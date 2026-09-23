package events

// Streams — the only topology.
//
// The authoritative stream → subject-filter mapping lives in topology.go
// (StreamFilters); natsx.EnsureTopology builds its stream configs from it.
const (
	StreamFiles = "FILE_EVENTS"
	StreamCmd   = "BLOCK_REFS_CMD"
	StreamEvt   = "BLOCK_REFS_EVT"
	StreamDLQ   = "DLQ"
)
