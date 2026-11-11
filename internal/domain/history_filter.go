package domain

// history_filter.go maps observed queues to the history workspace's match types.
// These browsing categories keep normal games separate from both ranked queues,
// and preserve separate ordinary and Mayhem ARAM scopes. Existing competitive
// eligibility still decides which raw matches contribute to aggregate statistics.

// HistoryKind selects the browsing category for one client-published queue identifier.
// Unrecognized identifiers remain under other modes. Categories retain raw custom
// records and do not determine eligibility for competitive summary calculations.
func HistoryKind(queue int) string {
	switch queue {
	case 420:
		return "solo"
	case 440:
		return "flex"
	case 400, 430, 480, 490:
		return "normal"
	case 450, 3200, 3210, 3220, 3230:
		return "aram"
	case 2400, 3270:
		return "mayhem"
	}
	return "other"
}
