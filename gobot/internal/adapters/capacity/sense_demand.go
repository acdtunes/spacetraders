package capacity

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
)

// playerContract is one contract-history row parsed for signal aggregation —
// the shared read the demand AND performance collectors both consume.
type playerContract struct {
	id          string
	payment     int // PaymentOnAccepted + PaymentOnFulfilled
	deliveries  []contract.Delivery
	lastUpdated time.Time
	hasTime     bool
}

// hubs returns the distinct delivery hubs of the contract.
func (c playerContract) hubs() []string {
	seen := make(map[string]struct{}, len(c.deliveries))
	out := make([]string, 0, len(c.deliveries))
	for _, d := range c.deliveries {
		if _, dup := seen[d.DestinationSymbol]; dup || d.DestinationSymbol == "" {
			continue
		}
		seen[d.DestinationSymbol] = struct{}{}
		out = append(out, d.DestinationSymbol)
	}
	return out
}

// loadContracts reads the player's full contract history. Rows with corrupt
// deliveries JSON are skipped (tolerant, mirroring the history repository's
// ContractGoodDemand); rows with an unparseable LastUpdated keep their
// deliveries but do not contribute to the observation window.
func (s *Sensor) loadContracts(ctx context.Context, playerID int) ([]playerContract, error) {
	var models []persistence.ContractModel
	if err := s.db.WithContext(ctx).Where("player_id = ?", playerID).Find(&models).Error; err != nil {
		return nil, err
	}
	contracts := make([]playerContract, 0, len(models))
	for _, m := range models {
		var deliveries []contract.Delivery
		if err := json.Unmarshal([]byte(m.DeliveriesJSON), &deliveries); err != nil {
			continue
		}
		row := playerContract{id: m.ID, payment: m.PaymentOnAccepted + m.PaymentOnFulfilled, deliveries: deliveries}
		if ts, err := time.Parse(time.RFC3339, m.LastUpdated); err == nil {
			row.lastUpdated = ts
			row.hasTime = true
		}
		contracts = append(contracts, row)
	}
	return contracts, nil
}

// senseDemand aggregates contract history into per-hub demand. Frequencies are
// contracts/hour over the recent-N COUNT window: the most recent
// N contracts and the wall-clock span THEY occupy — now → the oldest of the N,
// floored at 1h. The pre-fix window ran back to the FIRST contract the player
// ever completed, an ever-growing denominator that aged an established hub's
// frequency toward zero as history accumulated; a count window is structurally
// immune to that dilution.
func (s *Sensor) senseDemand(contracts []playerContract, now time.Time) domainCapacity.DemandSignals {
	if len(contracts) == 0 {
		return domainCapacity.DemandSignals{}
	}
	windowed, windowHours := recentContractWindow(contracts, now, s.demandWindowContracts)

	type goodAgg struct {
		contractCount int
		unitsSum      int
	}
	type hubAgg struct {
		contractCount int
		paymentSum    int
		goods         map[string]*goodAgg
	}
	byHub := make(map[string]*hubAgg)
	for _, c := range windowed {
		goodUnits := unitsByHubAndGood(c.deliveries)
		for _, hub := range c.hubs() {
			agg, ok := byHub[hub]
			if !ok {
				agg = &hubAgg{goods: make(map[string]*goodAgg)}
				byHub[hub] = agg
			}
			agg.contractCount++
			agg.paymentSum += c.payment
			for good, units := range goodUnits[hub] {
				g, ok := agg.goods[good]
				if !ok {
					g = &goodAgg{}
					agg.goods[good] = g
				}
				g.contractCount++
				g.unitsSum += units
			}
		}
	}

	hubs := make([]domainCapacity.HubDemand, 0, len(byHub))
	for hub, agg := range byHub {
		goodMix := make([]domainCapacity.GoodDemand, 0, len(agg.goods))
		for good, g := range agg.goods {
			goodMix = append(goodMix, domainCapacity.GoodDemand{
				Good:      good,
				Frequency: float64(g.contractCount) / windowHours,
				AvgUnits:  float64(g.unitsSum) / float64(g.contractCount),
			})
		}
		sort.Slice(goodMix, func(i, j int) bool { return goodMix[i].Good < goodMix[j].Good })
		hubs = append(hubs, domainCapacity.HubDemand{
			HubSymbol:         hub,
			ContractFrequency: float64(agg.contractCount) / windowHours,
			AvgPaymentCredits: float64(agg.paymentSum) / float64(agg.contractCount),
			GoodMix:           goodMix,
		})
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].HubSymbol < hubs[j].HubSymbol })
	return domainCapacity.DemandSignals{Hubs: hubs}
}

// recentContractWindow selects the most recent N contracts (by LastUpdated) and
// returns them alongside the window hours THEY span — now → the oldest of those
// N, floored at 1h. This is the COUNT window: capping the lookback at N
// contracts keeps the denominator bounded, so an established hub's frequency no
// longer decays toward zero as its history grows past N (an earlier window ran
// back to the FIRST contract ever, an ever-growing wall-clock span). Contracts
// with an unparseable timestamp cannot be time-ordered, so they are always
// retained — demand we cannot order must not silently vanish — but never widen
// the window. A non-positive windowCount disables the cap (every timed contract
// counts), preserving the pre-fix span for that degenerate configuration.
func recentContractWindow(contracts []playerContract, now time.Time, windowCount int) ([]playerContract, float64) {
	timed := make([]playerContract, 0, len(contracts))
	var untimed []playerContract
	for _, c := range contracts {
		if c.hasTime {
			timed = append(timed, c)
			continue
		}
		untimed = append(untimed, c)
	}
	sort.Slice(timed, func(i, j int) bool { return timed[i].lastUpdated.After(timed[j].lastUpdated) })
	if windowCount > 0 && len(timed) > windowCount {
		timed = timed[:windowCount]
	}
	windowed := make([]playerContract, 0, len(timed)+len(untimed))
	windowed = append(windowed, timed...)
	windowed = append(windowed, untimed...)
	return windowed, recentWindowHours(timed, now)
}

// recentWindowHours is now → the oldest of the recent-N timed contracts, floored
// at 1h so a single fresh contract never reads as infinite demand and the window
// never divides by less than an hour. No timed contracts ⇒ 1h.
func recentWindowHours(recentTimed []playerContract, now time.Time) float64 {
	var oldest time.Time
	for _, c := range recentTimed {
		if oldest.IsZero() || c.lastUpdated.Before(oldest) {
			oldest = c.lastUpdated
		}
	}
	if oldest.IsZero() {
		return 1
	}
	hours := now.Sub(oldest).Hours()
	if hours < 1 {
		return 1
	}
	return hours
}

// unitsByHubAndGood sums one contract's required units per (hub, good).
func unitsByHubAndGood(deliveries []contract.Delivery) map[string]map[string]int {
	out := make(map[string]map[string]int)
	for _, d := range deliveries {
		if d.DestinationSymbol == "" || d.TradeSymbol == "" {
			continue
		}
		if out[d.DestinationSymbol] == nil {
			out[d.DestinationSymbol] = make(map[string]int)
		}
		out[d.DestinationSymbol][d.TradeSymbol] += d.UnitsRequired
	}
	return out
}
