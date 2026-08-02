package commands

import "time"

func (c *RunOpportunityRelocatorCommand) tickInterval() time.Duration {
	return time.Duration(resolveRelocatorTickSeconds(c.TickSeconds)) * time.Second
}

func (c *RunOpportunityRelocatorCommand) npvThreshold() int64 {
	return resolveRelocatorNPVThreshold(c.NPVThresholdCredits)
}

func (c *RunOpportunityRelocatorCommand) upliftBarPct() int {
	return resolveRelocatorUpliftBarPct(c.UpliftBarPct)
}

func (c *RunOpportunityRelocatorCommand) maxConcurrent() int {
	return resolveRelocatorMaxConcurrent(c.MaxConcurrentRelocations)
}

func (c *RunOpportunityRelocatorCommand) hullCooldown() time.Duration {
	return time.Duration(resolveRelocatorCooldownMinutes(c.CooldownMinutes)) * time.Minute
}

func (c *RunOpportunityRelocatorCommand) horizonHours() int {
	return resolveRelocatorHorizonHours(c.HorizonHours)
}

func (c *RunOpportunityRelocatorCommand) riskMarginTourHours() float64 {
	return float64(resolveRelocatorRiskMarginTourMinutes(c.RiskMarginTourMinutes)) / 60
}

func (c *RunOpportunityRelocatorCommand) regionHopRadius() int {
	return resolveRelocatorRegionHopRadius(c.RegionHopRadius)
}

func (c *RunOpportunityRelocatorCommand) rateWindow() time.Duration {
	return time.Duration(resolveRelocatorRateWindowMinutes(c.RateWindowMinutes)) * time.Minute
}

func resolveRelocatorTickSeconds(configured int) int {
	if configured <= 0 {
		return defaultRelocatorTickSeconds
	}
	return configured
}

func resolveRelocatorNPVThreshold(configured int64) int64 {
	if configured <= 0 {
		return defaultRelocatorNPVThresholdCredits
	}
	return configured
}

// resolveRelocatorUpliftBarPct applies the 0/absent -> 150 rule AND clamps UP to
// relocatorUpliftBarPctMin: the anti-thrash ratchet can be raised, never weakened.
func resolveRelocatorUpliftBarPct(configured int) int {
	if configured <= 0 {
		return defaultRelocatorUpliftBarPct
	}
	if configured < relocatorUpliftBarPctMin {
		return relocatorUpliftBarPctMin
	}
	return configured
}

func resolveRelocatorMaxConcurrent(configured int) int {
	if configured <= 0 {
		return defaultRelocatorMaxConcurrentRelocations
	}
	return configured
}

func resolveRelocatorCooldownMinutes(configured int) int {
	if configured <= 0 {
		return defaultRelocatorCooldownMinutes
	}
	return configured
}

func resolveRelocatorHorizonHours(configured int) int {
	if configured <= 0 {
		return defaultRelocatorHorizonHours
	}
	return configured
}

func resolveRelocatorRiskMarginTourMinutes(configured int) int {
	if configured <= 0 {
		return defaultRelocatorRiskMarginTourMinutes
	}
	return configured
}

func resolveRelocatorRegionHopRadius(configured int) int {
	if configured <= 0 {
		return defaultRelocatorRegionHopRadius
	}
	return configured
}

func resolveRelocatorRateWindowMinutes(configured int) int {
	if configured <= 0 {
		return defaultRelocatorRateWindowMinutes
	}
	return configured
}
