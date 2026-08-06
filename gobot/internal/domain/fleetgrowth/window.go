package fleetgrowth

import "time"

// TradeCycleWindow is the trailing window every fleet-scale money observation is measured over.
//
// ONE CONSTANT, THREE CONSUMERS, and they all need the same physical property: at least one full
// trade cycle of history, so that a measurement taken mid-cycle is a measurement of the FLEET and
// not of the cycle's phase. The cargo-runway term needs it to be a rate rather than a snapshot;
// the hold-fill term needs it to have seen a representative purchase; the high-water treasury
// needs it to span a peak.
//
// THE SIZING CRITERION IS A PROPERTY, NOT A NUMBER: a peak lands inside the window only if the
// trade cycle is no longer than the window. Size it below the cycle and the high-water mark
// itself starts oscillating, which puts back exactly the flapping the reachability clause was
// changed to remove. Two differently-sized windows over one fleet is the same failure a step
// earlier — two spenders measuring one quantity and disagreeing about it.
//
// ITS LENGTH IS ALSO A UNIT, WHICH THE CRITERION ABOVE DOES NOT CONSTRAIN. The cargo-runway
// reader hands this window's SUM to a per-HOUR rate parameter unchanged, so that conversion is an
// identity only while the window is exactly one hour. Lengthen the window and the probe-buy
// floor's cargo term silently inflates; shorten it and the term deflates — a money guard weakened
// by editing a constant. Either way the sum must be normalised to a rate at that call site in the
// same commit.
const TradeCycleWindow = time.Hour
