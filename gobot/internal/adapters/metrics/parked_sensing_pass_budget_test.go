package metrics

import "testing"

// THE PAIR IS THE POINT. "used" alone cannot say whether a pass was bound or merely quiet,
// which is exactly the ambiguity that left 2.6% map coverage unexplained for six days, so
// both series are written by one call and neither can be published without the other.
func TestRecordPassBudget_PublishesUsedAndLimitTogether(t *testing.T) {
	c := NewParkedSensingMetricsCollector()

	c.RecordPassBudget(1, "gate", 17, 17)

	used := gaugeSamples(t, c.passBudgetUsed)
	limit := gaugeSamples(t, c.passBudgetLimit)
	if used["pass=gate;player_id=1;"] != 17 {
		t.Fatalf("used published %v, want 17 (%v)", used["pass=gate;player_id=1;"], used)
	}
	if limit["pass=gate;player_id=1;"] != 17 {
		t.Fatalf("limit published %v, want 17 (%v)", limit["pass=gate;player_id=1;"], limit)
	}
}

// A PASS THAT DRAINS ITS BACKLOG FALLS BACK TO ZERO on the same series. A gauge left
// standing at its last non-zero value would report a pass as permanently pinned to its
// budget long after it stopped binding — an operator would then raise a cap that is
// already free.
func TestRecordPassBudget_ADrainedPassFallsBackToZero(t *testing.T) {
	c := NewParkedSensingMetricsCollector()

	c.RecordPassBudget(1, "yards", 45, 45)
	c.RecordPassBudget(1, "yards", 0, 45)

	used := gaugeSamples(t, c.passBudgetUsed)
	if used["pass=yards;player_id=1;"] != 0 {
		t.Fatalf("a drained pass still reads %v, want 0 (%v)", used["pass=yards;player_id=1;"], used)
	}
	if limit := gaugeSamples(t, c.passBudgetLimit); limit["pass=yards;player_id=1;"] != 45 {
		t.Fatalf("the limit must keep reporting the budget it had, got %v", limit)
	}
}

// EACH PASS AND EACH PLAYER IS ITS OWN SERIES: the whole question is WHICH cap is binding,
// and a shared series would let one pass overwrite another's answer.
func TestRecordPassBudget_ScopedPerPassAndPlayer(t *testing.T) {
	c := NewParkedSensingMetricsCollector()

	c.RecordPassBudget(1, "gate", 17, 17)
	c.RecordPassBudget(1, "place", 3, 57)
	c.RecordPassBudget(2, "gate", 0, 3)

	used := gaugeSamples(t, c.passBudgetUsed)
	if used["pass=gate;player_id=1;"] != 17 || used["pass=place;player_id=1;"] != 3 || used["pass=gate;player_id=2;"] != 0 {
		t.Fatalf("passes or players are sharing a series: %v", used)
	}
}

// A NIL COLLECTOR RECORDS NOTHING AND PANICS AT NOTHING (RULINGS #4).
func TestRecordPassBudget_NilCollectorIsSafe(t *testing.T) {
	var c *ParkedSensingMetricsCollector
	c.RecordPassBudget(1, "gate", 17, 17)
}
