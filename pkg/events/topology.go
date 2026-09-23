package events

// StreamFilters is the single source of truth for which subjects each stream
// captures. pkg/natsx/topology.go builds every stream config from this map,
// and the contract tests assert every published subject is covered by exactly
// one filter here.
//
// EnsureTopology is the sole topology owner: both services call it at boot
// with this map, so start order between them never matters.
const DLQFilter = "dlq.>"

var StreamFilters = map[string][]string{
	StreamFiles: {"files.v2.>"},
	StreamCmd:   {SubjDecrefRequested},
	StreamEvt:   {SubjDecrefCompleted},
	StreamDLQ:   {DLQFilter},
}

// RetentionPolicy mirrors jetstream.RetentionPolicy so pkg/events does not
// need a NATS import.
type RetentionPolicy int

const (
	RetentionLimits RetentionPolicy = iota
	RetentionInterest
	RetentionWorkQueue
)
