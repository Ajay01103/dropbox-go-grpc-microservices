//go:build integration

// Pins the Tier B work index against real Scylla. stitched_objects is keyed by
// file_id, so asking it for "every file awaiting consolidation" was a
// WHERE state = ? scan, which Scylla refuses outright ("Cannot execute this
// query as it might involve data filtering"). Every stitcher sweep errored and
// no file was ever consolidated. stitched_objects_by_state answers that
// question per state partition instead; this test is what keeps the sweep
// query shape honest, because only a real server rejects an unfilterable query.
//
//	go test -tags integration ./internal/repository/...
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

// indexStatesFor lists every work-index partition currently holding the file.
// A file awaiting consolidation appears in exactly one because the index is
// what makes it visible to the sweeper.
func indexStatesFor(t *testing.T, ctx context.Context, session *gocql.Session, id gocql.UUID) []string {
	t.Helper()
	var states []string
	for _, state := range stitchIndexStates {
		var scanned gocql.UUID
		iter := session.Query(
			`SELECT file_id FROM stitched_objects_by_state WHERE state = ? AND file_id = ?`,
			string(state), id,
		).WithContext(ctx).Iter()
		found := 0
		for iter.Scan(&scanned) {
			found++
		}
		if err := iter.Close(); err != nil {
			t.Fatalf("read %s index partition: %v", state, err)
		}
		if found > 0 {
			states = append(states, string(state))
		}
	}
	return states
}

func TestIntegrationStitchWorkIndex(t *testing.T) {
	session := purgeTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	repo := NewStitchRepo(session)
	now := time.Now().UTC()
	first, second := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), first)
		_ = repo.Delete(context.Background(), second)
	})

	for _, fileID := range []string{first, second} {
		if err := repo.Enqueue(ctx, fileID, now); err != nil {
			t.Fatalf("Enqueue(%s): %v", fileID, err)
		}
	}

	// The sweep work list is a query the server must accept; this is the call
	// that returned "Cannot execute this query...use ALLOW FILTERING" before.
	assertListed := func(t *testing.T, wantState StitchState, fileIDs ...string) {
		t.Helper()
		records, err := repo.ListPending(ctx, 200)
		if err != nil {
			t.Fatalf("ListPending: %v", err)
		}
		listed := map[string]StitchState{}
		for _, rec := range records {
			listed[rec.FileID] = rec.State
		}
		for _, fileID := range fileIDs {
			if got, ok := listed[fileID]; !ok {
				t.Errorf("ListPending omitted %s (want state %s)", fileID, wantState)
			} else if got != wantState {
				t.Errorf("ListPending reported %s as %s, want %s", fileID, got, wantState)
			}
		}
	}

	assertListed(t, StitchStatePending, first, second)
	if states := indexStatesFor(t, ctx, session, mustTestUUID(t, first)); len(states) != 1 || states[0] != string(StitchStatePending) {
		t.Fatalf("index states after enqueue = %v, want [pending]", states)
	}

	// Claiming moves a file between index partitions.
	if _, ok, err := repo.ClaimForStitch(ctx, first, 10*time.Minute, now); err != nil || !ok {
		t.Fatalf("ClaimForStitch = (ok=%v, err=%v)", ok, err)
	}
	if states := indexStatesFor(t, ctx, session, mustTestUUID(t, first)); len(states) != 1 || states[0] != string(StitchStateStitching) {
		t.Fatalf("index states after claim = %v, want [stitching]", states)
	}

	// A failure stays visible: the next sweep has to be able to retry it.
	if err := repo.MarkFailed(ctx, first, now); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	assertListed(t, StitchStateFailed, first)
	if states := indexStatesFor(t, ctx, session, mustTestUUID(t, first)); len(states) != 1 || states[0] != string(StitchStateFailed) {
		t.Fatalf("index states after failure = %v, want [failed]", states)
	}

	// Consolidation retires the file: it must leave the work list for good.
	if _, ok, err := repo.ClaimForStitch(ctx, first, 10*time.Minute, now); err != nil || !ok {
		t.Fatalf("re-claim failed row = (ok=%v, err=%v)", ok, err)
	}
	if err := repo.MarkReady(ctx, first, "consolidated/"+first, `"etag"`, 12, now); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if states := indexStatesFor(t, ctx, session, mustTestUUID(t, first)); len(states) != 0 {
		t.Fatalf("index states after ready = %v, want none (ready work is finished)", states)
	}
	records, err := repo.ListPending(ctx, 200)
	if err != nil {
		t.Fatalf("ListPending after ready: %v", err)
	}
	for _, rec := range records {
		if rec.FileID == first {
			t.Fatalf("consolidated file %s reappeared in the sweep work list as %s", first, rec.State)
		}
	}

	// Deletion (the purge path) clears the registry and the index together.
	if err := repo.Delete(ctx, second); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if states := indexStatesFor(t, ctx, session, mustTestUUID(t, second)); len(states) != 0 {
		t.Fatalf("index states after delete = %v, want none", states)
	}
}
