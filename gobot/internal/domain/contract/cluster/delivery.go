package cluster

// SelectDeliveryHull returns the pinned delivery hull the cluster uses to fulfil a
// contract delivering to destinationSymbol. Selection is PURE CONFIGURATION: the
// mechanism returns the config-assigned delivery hull (the first configured, in
// config order) and does NOT prefer, favor, or special-case a hull that happens to
// be co-located at the destination.
//
// Co-location is whatever the config produced, never a built-in rule: if the analyst
// parked the config-assigned hull at the destination it delivers locally (~0 haul),
// but that is a property of the PLACEMENT, not of this selector. A co-located hull
// placed anywhere but first is NOT promoted over the config-assigned first hull.
// destinationSymbol is therefore not consulted by the current config-assigned-hull
// rule; it is retained so a future PURE-CONFIG rule (e.g. a config-driven per-
// destination assignment) can slot in without a signature change — never to
// reintroduce a co-location preference.
//
// Returns ok=false only when the cluster has no delivery hull at all — then no local
// delivery is possible and the caller keeps the legacy long haul.
func (c *ContractCluster) SelectDeliveryHull(destinationSymbol string) (Element, bool) {
	if len(c.deliveryHulls) == 0 {
		return Element{}, false
	}
	return c.deliveryHulls[0], true
}
