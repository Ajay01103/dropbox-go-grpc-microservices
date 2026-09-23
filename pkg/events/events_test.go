package events

import (
	"strings"
	"testing"
)

// ─── Contract: subject → stream coverage ─────────────────────────────────────
//
// Every published subject must be captured by exactly one stream's subject
// filter, or messages silently go nowhere (or to two places at once).

// v2Subjects are the subjects published in the B4 topology. The stream
// definitions come from StreamFilters — the single source of truth shared
// with natsx.EnsureTopology (which since B4 is the sole topology owner).
var (
	v2Subjects = []string{SubjFileStored, SubjDecrefRequested, SubjDecrefCompleted}

	v2StreamNames = []string{StreamFiles, StreamCmd, StreamEvt, StreamDLQ}
)

func matchCount(filters []string, subject string) int {
	n := 0
	for _, f := range filters {
		if subjectMatchesFilter(f, subject) {
			n++
		}
	}
	return n
}

// subjectMatchesFilter implements the subset of NATS subject-matching used by
// these constants: literal tokens and a single trailing ">" wildcard.
func subjectMatchesFilter(filter, subject string) bool {
	if strings.HasSuffix(filter, ">") {
		prefix := strings.TrimSuffix(filter, ">")
		if !strings.HasSuffix(prefix, ".") {
			prefix += "."
		}
		return strings.HasPrefix(subject+".", prefix) || strings.HasPrefix(subject, strings.TrimSuffix(prefix, ".")+".")
	}
	return filter == subject
}

func TestEverySubjectCoveredByExactlyOneStream(t *testing.T) {
	for _, subj := range v2Subjects {
		count := 0
		for _, name := range v2StreamNames {
			count += matchCount(StreamFilters[name], subj)
		}
		if count != 1 {
			t.Errorf("subject %s covered by %d stream filters, want exactly 1", subj, count)
		}
	}
}

func TestEveryStreamFilterIsValid(t *testing.T) {
	for name, filters := range StreamFilters {
		if len(filters) == 0 {
			t.Errorf("stream %s has no subject filters", name)
		}
		for _, f := range filters {
			if f == "" || strings.ContainsAny(f, " ") {
				t.Errorf("stream %s has invalid filter %q", name, f)
			}
		}
	}
}

// ─── Contract: constants are well-formed ────────────────────────────────────

func TestSubjectsAreValidNATSSubjects(t *testing.T) {
	all := map[string]string{
		SubjFileStored:      "SubjFileStored",
		SubjDecrefRequested: "SubjDecrefRequested",
		SubjDecrefCompleted: "SubjDecrefCompleted",
	}
	for val, name := range all {
		if val == "" {
			t.Errorf("%s is empty", name)
			continue
		}
		if strings.ContainsAny(val, " \t*") {
			t.Errorf("%s = %q contains forbidden characters (space, tab or '*' wildcard)", name, val)
		}
		for _, tok := range strings.Split(val, ".") {
			if tok == "" {
				t.Errorf("%s = %q has an empty subject token", name, val)
			}
		}
	}
}

func TestStreamAndConsumerNamesNonEmpty(t *testing.T) {
	nonEmpty := []string{
		StreamFiles, StreamCmd, StreamEvt, StreamDLQ,
		ConsumerThumbnailV2, ConsumerDecrefV2, ConsumerPurgeCompletionV2,
	}
	for _, s := range nonEmpty {
		if s == "" {
			t.Error("stream/consumer constant is empty")
		}
	}
}

// ─── Contract: MsgID builders ────────────────────────────────────────────────

func TestMsgIDDecrefBuilders(t *testing.T) {
	if got, want := MsgIDDecrefRequested("job", 2), "decref:job:2"; got != want {
		t.Errorf("MsgIDDecrefRequested = %q, want %q", got, want)
	}
	if got, want := MsgIDDecrefRequestedRedrive("job", 2, 3), "decref:job:2:r3"; got != want {
		t.Errorf("MsgIDDecrefRequestedRedrive = %q, want %q", got, want)
	}
	if got, want := MsgIDDecrefCompleted("job", 2), "decref-done:job:2"; got != want {
		t.Errorf("MsgIDDecrefCompleted = %q, want %q", got, want)
	}
}
