package main

import (
	"testing"
	"time"
)

// ─── subject coverage (the dlq-replay refusal rule) ─────────────────────────

func TestSubjectCovered(t *testing.T) {
	covered := []string{
		"files.v2.stored",
		"blocks.v2.decref.requested",
		"blocks.v2.decref.completed",
		"dlq.BLOCK_REFS_CMD.decref-v2", // matches the DLQ stream's dlq.> filter
		"dlq.anything",
	}
	for _, s := range covered {
		if !subjectCovered(s) {
			t.Errorf("subjectCovered(%q) = false, want true", s)
		}
	}

	// Subjects no stream covers: dlq-replay must refuse to republish them.
	uncovered := []string{
		"uploads.object.stored",
		"blocks.refs.decrement.requested",
		"blocks.refs.decrement.completed",
		"blocks.refs.anything",
		"totally.unknown",
		"",
	}
	for _, s := range uncovered {
		if subjectCovered(s) {
			t.Errorf("subjectCovered(%q) = true, want false", s)
		}
	}
}

func TestFilterMatches(t *testing.T) {
	cases := []struct {
		filter, subject string
		want            bool
	}{
		{"files.v2.>", "files.v2.stored", true},
		{"files.v2.>", "files.v2.stored.extra", true},
		{"files.v2.>", "files.v1.stored", false},
		{"files.v2.stored", "files.v2.stored", true},
		{"files.v2.stored", "files.v2.stored.x", false},
		{"blocks.v2.>", "blocks.v2.decref.requested", true},
		{"*.v2.stored", "files.v2.stored", true},
		{"*.v2.stored", "files.v2.decref", false},
		{"dlq.>", "dlq.X.Y", true},
		{"a.>", "a", false}, // ">" needs at least the tokens before it
	}
	for _, tc := range cases {
		if got := filterMatches(tc.filter, tc.subject); got != tc.want {
			t.Errorf("filterMatches(%q, %q) = %v, want %v", tc.filter, tc.subject, got, tc.want)
		}
	}
}

// ─── replay MsgId suffixing (the dedup-swallow trap) ────────────────────────

func TestReplayMsgIDSuffixesOriginal(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	if got, want := replayMsgID("stored:abc:1", now), "stored:abc:1:replay1700000000"; got != want {
		t.Errorf("replayMsgID with original = %q, want %q", got, want)
	}
	if got, want := replayMsgID("", now), "replay:1700000000"; got != want {
		t.Errorf("replayMsgID without original = %q, want %q", got, want)
	}
}

// TestReplayMsgIDDiffersFromOriginal pins the failure class this prevents: a
// replayed message carrying its ORIGINAL MsgId would be dedup-suppressed
// inside the server window and silently vanish.
func TestReplayMsgIDDiffersFromOriginal(t *testing.T) {
	original := "decref:job:0"
	if got, want := replayMsgID(original, time.Now()), original; got == want {
		t.Fatal("replay MsgId must differ from the original MsgId")
	}
}
