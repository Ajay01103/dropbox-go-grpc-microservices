package natsx

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ajay01103/go-dropbox/pkg/events"
)

// TestEnsureTopologySpecs pins the stream configs EnsureTopology will create,
// derived from events.StreamFilters. If someone edits the shared filter map,
// this test forces them to notice the retention/age implications.
func TestEnsureTopologySpecs(t *testing.T) {
	want := map[string]struct {
		subjects  []string
		retention jetstream.RetentionPolicy
		maxAge    time.Duration
		dedup     time.Duration
	}{
		events.StreamFiles: {
			subjects: []string{"files.v2.>"}, retention: jetstream.LimitsPolicy,
			maxAge: 7 * 24 * time.Hour, dedup: time.Hour,
		},
		events.StreamCmd: {
			subjects: []string{events.SubjDecrefRequested}, retention: jetstream.WorkQueuePolicy,
			maxAge: 7 * 24 * time.Hour, dedup: time.Hour,
		},
		events.StreamEvt: {
			subjects: []string{events.SubjDecrefCompleted}, retention: jetstream.LimitsPolicy,
			maxAge: 7 * 24 * time.Hour, dedup: time.Hour,
		},
		events.StreamDLQ: {
			subjects: []string{"dlq.>"}, retention: jetstream.LimitsPolicy,
			maxAge: 14 * 24 * time.Hour, dedup: 0,
		},
	}

	gotSubjects, ok := events.StreamFilters[events.StreamCmd]
	if !ok || len(gotSubjects) != 1 || gotSubjects[0] != events.SubjDecrefRequested {
		t.Fatalf("events.StreamFilters[%s] = %v, want [%s]", events.StreamCmd, gotSubjects, events.SubjDecrefRequested)
	}

	for name, w := range want {
		filters, ok := events.StreamFilters[name]
		if !ok {
			t.Errorf("events.StreamFilters missing stream %s", name)
			continue
		}
		if len(filters) != len(w.subjects) {
			t.Errorf("stream %s filters = %v, want %v", name, filters, w.subjects)
			continue
		}
		for i := range filters {
			if filters[i] != w.subjects[i] {
				t.Errorf("stream %s filter[%d] = %q, want %q", name, i, filters[i], w.subjects[i])
			}
		}
		if got := retentionOf(retentionByStream(name)); got != w.retention {
			t.Errorf("stream %s retention = %v, want %v", name, got, w.retention)
		}
	}

	// The topology is exactly the four v2 streams — nothing more.
	if len(events.StreamFilters) != 4 {
		t.Errorf("events.StreamFilters has %d entries, want exactly the 4 streams", len(events.StreamFilters))
	}
}
