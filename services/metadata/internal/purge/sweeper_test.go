package purge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// ---- mock deps -------------------------------------------------------------

type mockDeps struct {
	refs        []repository.ActiveJobRef
	jobs        map[string]*repository.PurgeJob
	claimExpect map[string]int // jobID -> expectedAttempts that succeeds; absent = any
	// publishes records every PublishRequest call: PENDING dispatches and
	// REQUESTED re-drives both go through it .
	publishes []struct {
		jobID   string
		attempt int
	}
	removed []string
	states  map[string]string
	errs    []string
	listErr error
	getErr  error
}

func newMockDeps() *mockDeps {
	return &mockDeps{
		jobs:        map[string]*repository.PurgeJob{},
		claimExpect: map[string]int{},
		states:      map[string]string{},
	}
}

func (m *mockDeps) ListDueActive(_ context.Context, _ []string, _ time.Time) ([]repository.ActiveJobRef, error) {
	return m.refs, m.listErr
}

// ClaimJob succeeds only when the stored job still has expectedAttempts
// (mirroring the LWT's IF attempts = ?) and no conflicting expectation is
// registered. On success it mutates the stored row — the post-claim Get
// must see the bumped counter, exactly like the real LWT + read.
func (m *mockDeps) ClaimJob(_ context.Context, jobID string, expectedAttempts int, _ time.Time) (bool, error) {
	if want, ok := m.claimExpect[jobID]; ok && want != expectedAttempts {
		return false, nil // concurrent winner
	}
	if job := m.jobs[jobID]; job != nil {
		if job.Attempts != expectedAttempts {
			return false, nil
		}
		job.Attempts++
	}
	return true, nil
}

func (m *mockDeps) Get(_ context.Context, jobID string) (repository.PurgeJob, error) {
	if m.getErr != nil {
		return repository.PurgeJob{}, m.getErr
	}
	job, ok := m.jobs[jobID]
	if !ok {
		return repository.PurgeJob{}, errors.New("purge job not found")
	}
	return *job, nil
}

func (m *mockDeps) UpdateState(_ context.Context, job repository.PurgeJob, state string, lastError string) error {
	m.states[job.JobID] = state
	if job, ok := m.jobs[job.JobID]; ok {
		job.State = state
		job.LastError = lastError
	}
	return nil
}

func (m *mockDeps) RecordError(_ context.Context, jobID string, lastError string) error {
	m.errs = append(m.errs, jobID+":"+lastError)
	return nil
}

func (m *mockDeps) PublishRequest(_ context.Context, job repository.PurgeJob, attempt int) error {
	m.publishes = append(m.publishes, struct {
		jobID   string
		attempt int
	}{job.JobID, attempt})
	return nil
}

func (m *mockDeps) RemoveMetadataAndComplete(_ context.Context, job repository.PurgeJob) error {
	m.removed = append(m.removed, job.JobID)
	return nil
}

func (m *mockDeps) lastPublish() (jobID string, attempt int) {
	if len(m.publishes) == 0 {
		return "", -1
	}
	p := m.publishes[len(m.publishes)-1]
	return p.jobID, p.attempt
}

func testJob(id, state string, attempts int) *repository.PurgeJob {
	return &repository.PurgeJob{
		JobID: id, State: state, Attempts: attempts,
		FileID: "f-" + id, OwnerID: "o-" + id,
		BlockHashList: []string{"aabb", "ccdd"},
	}
}

func cfg() SweeperConfig {
	return SweeperConfig{
		Interval: time.Minute, InitialRetryDelay: time.Minute,
		MaxRetryDelay: time.Hour, BackoffMultiplier: 2.0, MaxAttempts: 10,
	}
}

// ---- dispatch table --------------------------------------------------------

// TestDispatchPublishesEveryState pins the dispatch routing:
// PENDING and REQUESTED both publish via PublishRequest (the only
// publisher since B4), with the post-claim attempts counter as the re-drive
// attempt — the same counter the MaxAttempts gate sees.
func TestDispatchPublishesEveryState(t *testing.T) {
	t.Run("PENDING publishes with post-claim attempt", func(t *testing.T) {
		m := newMockDeps()
		m.jobs["j1"] = testJob("j1", repository.PurgePending, 1)
		s := NewSweeper(m, cfg(), nil)
		if err := s.dispatch(context.Background(), *m.jobs["j1"]); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if len(m.publishes) != 1 || m.publishes[0].jobID != "j1" || m.publishes[0].attempt != 1 {
			t.Fatalf("PENDING re-drive = %+v, want one publish for j1 attempt 1", m.publishes)
		}
		if m.states["j1"] != repository.PurgeDecrementRequested {
			t.Fatalf("state after dispatch = %q, want REQUESTED", m.states["j1"])
		}
	})

	t.Run("REQUESTED re-drive reads the post-claim attempts counter", func(t *testing.T) {
		// End-to-end through recover(): the claim bumps attempts 3→4, and the
		// re-drive attempt must be 4, not 3 — the same counter the
		// MaxAttempts gate sees.
		m := newMockDeps()
		m.jobs["j3"] = testJob("j3", repository.PurgeDecrementRequested, 3)
		m.claimExpect["j3"] = 3
		m.refs = []repository.ActiveJobRef{{Bucket: "b", JobID: "j3", State: repository.PurgeDecrementRequested}}
		s := NewSweeper(m, cfg(), nil)
		s.sweepOnce(context.Background())
		jobID, attempt := m.lastPublish()
		if len(m.publishes) != 1 || jobID != "j3" || attempt != 4 {
			t.Fatalf("post-claim re-drive = %q/%d, want j3/4", jobID, attempt)
		}
	})
}

// TestDispatchRequestedBatchedRedrivesMissingBatchesOnly: a REQUESTED v2 job
// with batch_count set re-drives via PublishRequest — the coordinator then
// republishes ONLY the missing batches with attempt-suffixed MsgIds
// (decref:<job>:<batch>:r<attempt>).
func TestDispatchRequestedBatchedRedrivesMissingBatchesOnly(t *testing.T) {
	m := newMockDeps()
	m.jobs["j2"] = func() *repository.PurgeJob {
		j := testJob("j2", repository.PurgeDecrementRequested, 4)
		j.BatchCount = 3
		j.BatchesDone = []int{0}
		return j
	}()
	s := NewSweeper(m, cfg(), nil)
	if err := s.dispatch(context.Background(), *m.jobs["j2"]); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.publishes) != 1 || m.publishes[0].attempt != 4 {
		t.Fatalf("re-drive = %+v, want attempt 4 (post-claim)", m.publishes)
	}
}

// TestDispatchRequestedDoesNotAdvanceState pins that a REQUESTED re-drive
// republishes without touching state — the completion path owns transitions.
func TestDispatchRequestedDoesNotAdvanceState(t *testing.T) {
	m := newMockDeps()
	job := testJob("j1", repository.PurgeDecrementRequested, 2)
	m.jobs["j1"] = job
	s := NewSweeper(m, cfg(), nil)
	if err := s.dispatch(context.Background(), *job); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.publishes) != 1 {
		t.Fatalf("want exactly one republish, got %d", len(m.publishes))
	}
	if state, ok := m.states["j1"]; ok {
		t.Fatalf("REQUESTED re-drive must not change state, got %q", state)
	}
}

// TestDispatchDecrementedFinishesMetadata pins the DECREMENTED re-drive.
func TestDispatchDecrementedFinishesMetadata(t *testing.T) {
	m := newMockDeps()
	job := testJob("j1", repository.PurgeDecremented, 2)
	m.jobs["j1"] = job
	s := NewSweeper(m, cfg(), nil)
	if err := s.dispatch(context.Background(), *job); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.removed) != 1 || m.removed[0] != "j1" {
		t.Fatal("DECREMENTED job must be finished via RemoveMetadataAndComplete")
	}
}

// ---- claim / max-attempts --------------------------------------------------

// TestRecoverSkipsWhenClaimLost pins multi-instance safety: the sweeper that
// loses the LWT race does not dispatch.
func TestRecoverSkipsWhenClaimLost(t *testing.T) {
	m := newMockDeps()
	m.jobs["j1"] = testJob("j1", repository.PurgeDecrementRequested, 2)
	m.claimExpect["j1"] = 999 // claim expectation mismatches the stored attempts → lost
	m.refs = []repository.ActiveJobRef{{Bucket: "b", JobID: "j1", State: repository.PurgeDecrementRequested}}
	s := NewSweeper(m, cfg(), nil)
	s.sweepOnce(context.Background())
	if len(m.publishes) != 0 {
		t.Fatal("lost claim must not dispatch")
	}
}

// TestRecoverSkipsMissingJobRow covers the TTL'd-out-row path.
func TestRecoverSkipsMissingJobRow(t *testing.T) {
	m := newMockDeps()
	m.getErr = errors.New("purge job not found")
	m.refs = []repository.ActiveJobRef{{Bucket: "b", JobID: "ghost", State: repository.PurgePending}}
	s := NewSweeper(m, cfg(), nil)
	s.sweepOnce(context.Background())
	if len(m.publishes) != 0 {
		t.Fatal("missing row must not dispatch")
	}
}

// TestMaxAttemptsGate pins the FAILED transition: a job at the attempt budget
// is failed, never re-driven, and gets one ERROR log path via UpdateState.
func TestMaxAttemptsGate(t *testing.T) {
	m := newMockDeps()
	job := testJob("j1", repository.PurgeDecrementRequested, 10) // == MaxAttempts
	m.jobs["j1"] = job
	s := NewSweeper(m, cfg(), nil)
	if err := s.dispatch(context.Background(), *job); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.publishes) != 0 {
		t.Fatal("job at max attempts must not be re-driven")
	}
	if m.states["j1"] != repository.PurgeFailed {
		t.Fatalf("state = %q, want FAILED", m.states["j1"])
	}
	if !strings.Contains(job.LastError, "max attempts") {
		t.Errorf("last_error should explain the failure, got %q", job.LastError)
	}
}

// TestDispatchFailureRecordsError covers the NATS-down path: the publish
// fails, RecordError captures it, and the job stays scheduled (the claim
// already advanced next_attempt_at) — retried with backoff, not lost.
func TestDispatchFailureRecordsError(t *testing.T) {
	m := newMockDeps()
	job := testJob("j1", repository.PurgeDecrementRequested, 2)
	m.jobs["j1"] = job
	deps := &failingPublish{mockDeps: m, fail: true}
	s := NewSweeper(deps, cfg(), nil)
	err := s.dispatch(context.Background(), *job)
	if err == nil {
		t.Fatal("want dispatch error when publish fails")
	}
	if len(deps.errs) != 1 {
		t.Fatalf("RecordError must be called on dispatch failure, got %d calls", len(deps.errs))
	}
}

type failingPublish struct {
	*mockDeps
	fail bool
}

func (f *failingPublish) PublishRequest(_ context.Context, _ repository.PurgeJob, _ int) error {
	if f.fail {
		return errors.New("nats: connection closed")
	}
	return f.mockDeps.PublishRequest(context.Background(), repository.PurgeJob{}, 0)
}

// ---- backoff ---------------------------------------------------------------

func TestComputeBackoff(t *testing.T) {
	c := cfg()
	base := c.InitialRetryDelay

	// base=1m, cap=1h: the cap is first hit between attempts 7 (64m) and 8.
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, base},             // first retry: base delay
		{2, 2 * base},         // ×2
		{3, 4 * base},         // ×4
		{5, 16 * base},        // ×16
		{6, 32 * base},        // ×32 — still under the cap
		{7, c.MaxRetryDelay},  // 64m → capped
		{50, c.MaxRetryDelay}, // stays capped
	}
	for _, tc := range cases {
		job := repository.PurgeJob{Attempts: tc.attempts}
		if got := ComputeBackoff(job, c); got != tc.want {
			t.Errorf("ComputeBackoff(attempts=%d) = %v, want %v", tc.attempts, got, tc.want)
		}
	}
}

// ---- bucket lookback -------------------------------------------------------

// TestDueBucketsCoversWorstCaseLifetime pins the lookback sizing rule: the
// window must span at least MaxAttempts × MaxRetryDelay (+ margin), so a job
// that keeps hitting the backoff cap stays visible to the sweeper until it
// either completes or is FAILED — it can never silently drop out of the scan.
func TestDueBucketsCoversWorstCaseLifetime(t *testing.T) {
	c := cfg()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	buckets := DueBuckets(now, c)

	// With MaxAttempts=10 and MaxRetryDelay=1h the rule requires ≥ 10+2 hours,
	// floored at 24.
	if len(buckets) < 24 {
		t.Fatalf("bucket window = %d hours, want ≥ 24", len(buckets))
	}

	// The worst-case job lifetime: 10 retries spaced 1h apart = 10h. A job
	// created at the oldest bucket boundary must still be inside the window.
	lifetime := time.Duration(c.MaxAttempts) * c.MaxRetryDelay
	window := time.Duration(len(buckets)) * time.Hour
	if lifetime > window {
		t.Fatalf("worst-case job lifetime %v exceeds scan window %v — due jobs would go invisible", lifetime, window)
	}
}

// TestDueBucketsStrictlyDerived pins that growing the retry config grows the
// window: the lookback is derived from config, not hardcoded.
func TestDueBucketsStrictlyDerived(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	small := DueBuckets(now, SweeperConfig{MaxAttempts: 5, MaxRetryDelay: 30 * time.Minute})
	big := DueBuckets(now, SweeperConfig{MaxAttempts: 100, MaxRetryDelay: 6 * time.Hour})
	if len(big) <= len(small) {
		t.Fatalf("larger retry budget must widen the scan window: %d vs %d", len(big), len(small))
	}
}

// TestDueBucketsAllDistinctHours guards format/hour-boundary bugs.
func TestDueBucketsAllDistinctHours(t *testing.T) {
	now := time.Date(2026, 9, 19, 0, 30, 0, 0, time.UTC) // crosses midnight/month locally-safe
	buckets := DueBuckets(now, cfg())
	seen := map[string]bool{}
	for _, b := range buckets {
		if seen[b] {
			t.Fatalf("duplicate bucket %q", b)
		}
		seen[b] = true
	}
	if buckets[0] != "2026091900" {
		t.Fatalf("first bucket = %q, want current hour", buckets[0])
	}
}

// ---- legacy-state safety net ----------------------------------------------

// TestSweeperHandlesLegacyState pins the B4 safety net: a job in a legacy
// pre-B4 state that the backfill missed is normalized on read (repo.Get) and
// handled exactly like its modern equivalent.
func TestSweeperHandlesLegacyState(t *testing.T) {
	m := newMockDeps()
	// DECREMENTED_WITH_ERRORS → DECREMENTED → RemoveMetadataAndComplete.
	m.jobs["j1"] = testJob("j1", repository.ParsePurgeState("DECREMENTED_WITH_ERRORS"), 2)
	s := NewSweeper(m, cfg(), nil)
	if err := s.dispatch(context.Background(), *m.jobs["j1"]); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.removed) != 1 || m.removed[0] != "j1" {
		t.Fatal("legacy DECREMENTED_WITH_ERRORS job must be finished like DECREMENTED")
	}

	m = newMockDeps()
	// METADATA_REMOVED → COMPLETE → nothing to do.
	m.jobs["j2"] = testJob("j2", repository.ParsePurgeState("METADATA_REMOVED"), 2)
	s = NewSweeper(m, cfg(), nil)
	if err := s.dispatch(context.Background(), *m.jobs["j2"]); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(m.removed) != 0 || len(m.publishes) != 0 {
		t.Fatal("legacy METADATA_REMOVED job (already COMPLETE) must not be re-driven")
	}
}
