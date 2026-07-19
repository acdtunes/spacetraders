package system

// SystemAPIData is one system from the universe GET /systems list: its symbol, galaxy
// coordinates, and type. Unlike WaypointAPIData (a waypoint WITHIN a system), these are
// SYSTEM-level galaxy coordinates — the basis for inter-system WARP distance in the
// off-gate explorer target selection. Mirrors WaypointAPIData's shape.
type SystemAPIData struct {
	Symbol string
	Type   string
	X      float64
	Y      float64
}

// SystemsListResponse is one page of the paginated universe system list (GET /systems),
// mirroring WaypointsListResponse. Meta carries the pagination cursor the caller loops on
// to crawl the whole universe once (the list is near-static within an era, so a bounded
// cache serves it thereafter).
type SystemsListResponse struct {
	Data []SystemAPIData
	Meta PaginationMeta
}
