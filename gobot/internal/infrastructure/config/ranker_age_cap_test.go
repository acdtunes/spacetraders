package config_test

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// The activity-conditioned caps are READ from the [trading] ranker_age_cap_minutes
// tune knobs (RULINGS #5), each falling back to the fitted default DEFINED ONCE in
// domain/trading — not a hardcoded 4-way constant in the ranker. This proves an absent
// section arms the analyst's fit, an explicit section retunes it, and a partial section
// keeps its set knob while defaulting the rest.
func TestRankerAgeCapConfig_ResolvesKnobsAndDefaults(t *testing.T) {
	// Absent section (zero config) resolves to the fitted armed defaults.
	absent := config.RankerAgeCapConfig{}.Resolved()
	if absent.For("WEAK") != trading.DefaultRankerAgeCapWeak ||
		absent.For("RESTRICTED") != trading.DefaultRankerAgeCapRestricted ||
		absent.For("GROWING") != trading.DefaultRankerAgeCapGrowing ||
		absent.For("STRONG") != trading.DefaultRankerAgeCapStrong {
		t.Fatalf("absent config must resolve to the fitted defaults, got %+v", absent)
	}

	// Explicit knobs override per activity (minutes -> Duration).
	tuned := config.RankerAgeCapConfig{Weak: 600, Restricted: 200, Growing: 45, Strong: 15}.Resolved()
	if tuned.For("WEAK") != 600*time.Minute || tuned.For("RESTRICTED") != 200*time.Minute ||
		tuned.For("GROWING") != 45*time.Minute || tuned.For("STRONG") != 15*time.Minute {
		t.Fatalf("explicit knobs must override the defaults, got %+v", tuned)
	}

	// A partially-set section keeps its set knob and defaults the rest (defined-once).
	partial := config.RankerAgeCapConfig{Strong: 10}.Resolved()
	if partial.For("STRONG") != 10*time.Minute || partial.For("WEAK") != trading.DefaultRankerAgeCapWeak {
		t.Fatalf("partial config must keep the set knob and default the rest, got %+v", partial)
	}
}
