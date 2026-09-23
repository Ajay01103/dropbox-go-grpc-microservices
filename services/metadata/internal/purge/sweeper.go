package purge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// SweeperConfig carries the retry knobs (all already in config.Config).
type SweeperConfig struct {
	Interval          time.Duration // PURGE_RECOVERY_INTERVAL
	InitialRetryDelay time.Duration // PURGE_INITIAL_RETRY_DELAY
	MaxRetryDelay     time.Duration // PURGE_MAX_RETRY_DELAY
	BackoffMultiplier float64       // PURGE_RETRY_BACKOFF_MULTIPLIER
	MaxAttempts       int           // PURGE_MAX_ATTEMPTS
}

// SweeperDeps is everything the sweeper needs. The concrete implementations
// live on PurgeRepo and Coordinator; tests supply mocks, which is what makes
// the dispatch-failure path (RecordError) and the claim race testable without
// Scylla or NATS.
type SweeperDeps interface {
	ListDueActive(ctx context.Context, buckets []string, now time.Time) ([]repository.ActiveJobRef, error)
	ClaimJob(ctx context.Context, jobID string, expectedAttempts int, nextAttemptAt time.Time) (bool, error)
	Get(ctx context.Context, jobID string) (repository.PurgeJob, error)
	UpdateState(ctx context.Context, job repository.PurgeJob, state string, lastError string) error
	RecordError(ctx context.Context, jobID string, lastError string) error
	// PublishRequest re-drives a job: PENDING (batch_count unset) →
	// publish all batches with their plain per-batch MsgIds; REQUESTED →
	// re-publish only missing batches with decref:<job>:<batch>:r<attempt>.
	PublishRequest(ctx context.Context, job repository.PurgeJob, attempt int) error
	RemoveMetadataAndComplete(ctx context.Context, job repository.PurgeJob) error
}

// Sweeper re-drives purge jobs that stalled: a coordinator crash between the
// job insert and the request publish, a lost request message, or a lost
// completion. It scans purge_jobs_active every Interval, claims due jobs via
// an LWT (multi-instance safe), and dispatches by state:
//
//	PENDING              → republish with the ORIGINAL per-batch MsgIds (a
//	                       duplicate of a publish that actually happened is
//	                       dedup-suppressed — the desired outcome)
//	DECREMENT_REQUESTED  → republish missing batches with
//	                       "decref:<job>:<batch>:r<attempt>" (a genuine
//	                       loss-retry MUST have a distinct MsgId or the dedup
//	                       window silently swallows it)
//	DECREMENTED          → RemoveMetadataAndComplete (idempotent)
//	attempts ≥ MaxAttempts → FAILED + last_error + one ERROR log
type Sweeper struct {
	deps SweeperDeps
	cfg  SweeperConfig
	log  *zap.Logger
}

func NewSweeper(deps SweeperDeps, cfg SweeperConfig, log *zap.Logger) *Sweeper {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.InitialRetryDelay <= 0 {
		cfg.InitialRetryDelay = time.Minute
	}
	if cfg.MaxRetryDelay <= 0 {
		cfg.MaxRetryDelay = time.Hour
	}
	if cfg.BackoffMultiplier < 1 {
		cfg.BackoffMultiplier = 2.0
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Sweeper{deps: deps, cfg: cfg, log: log}
}

// Start runs the sweep loop in the background; it stops when ctx is done.
// One immediate sweep covers jobs that came due while the service was down.
func (s *Sweeper) Start(ctx context.Context) {
	go func() {
		s.sweepOnce(ctx)
		t := time.NewTicker(s.cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.sweepOnce(ctx)
			}
		}
	}()
}

// sweepOnce is one scan/claim/dispatch pass. Errors per job are logged and
// do not abort the pass.
func (s *Sweeper) sweepOnce(ctx context.Context) {
	now := time.Now().UTC()
	refs, err := s.deps.ListDueActive(ctx, DueBuckets(now, s.cfg), now)
	if err != nil {
		s.log.Error("purge sweeper: list due jobs", zap.Error(err))
		return
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		if err := s.recover(ctx, ref, now); err != nil {
			s.log.Warn("purge sweeper: recover attempt failed (will retry on schedule)",
				zap.String("jobID", ref.JobID), zap.String("state", ref.State), zap.Error(err))
		}
	}
}

// recover claims one due job and dispatches it by state.
func (s *Sweeper) recover(ctx context.Context, ref repository.ActiveJobRef, now time.Time) error {
	// Read the current attempts BEFORE the claim so the LWT has its expected
	// value. We do NOT dispatch off this read — dispatch happens off the
	// post-claim Get so the MsgId's r<attempt> and the MaxAttempts gate use
	// the same bumped counter.
	pre, err := s.deps.Get(ctx, ref.JobID)
	if err != nil {
		// Row gone (7d TTL while stuck in the work list, or manual cleanup):
		// nothing to recover. Not an error — but keep the log for visibility.
		if strings.Contains(err.Error(), "purge job not found") {
			s.log.Info("purge sweeper: job row gone, skipping", zap.String("jobID", ref.JobID))
			return nil
		}
		return fmt.Errorf("get job pre-claim: %w", err)
	}

	// Claim first (attempts+1, next_attempt_at=now+backoff): concurrent
	// sweepers can't double-dispatch, and a dispatch failure still leaves the
	// job scheduled for retry.
	claimed, err := s.deps.ClaimJob(ctx, ref.JobID, pre.Attempts, now.Add(ComputeBackoff(pre, s.cfg)))
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if !claimed {
		return nil // another sweeper won
	}

	// Dispatch off the POST-claim row: Attempts is now pre.Attempts+1, and
	// both the re-drive MsgId and the MaxAttempts gate must see that value.
	job, err := s.deps.Get(ctx, ref.JobID)
	if err != nil {
		return fmt.Errorf("get job post-claim: %w", err)
	}
	return s.dispatch(ctx, job)
}

// dispatch performs the state-specific recovery action.
func (s *Sweeper) dispatch(ctx context.Context, job repository.PurgeJob) error {
	// Gate BEFORE dispatching: a job that just exceeded its attempt budget is
	// failed, not re-driven. Post-claim attempts includes the claim this pass
	// just made.
	if job.Attempts >= s.cfg.MaxAttempts {
		if err := s.deps.UpdateState(ctx, job, repository.PurgeFailed,
			fmt.Sprintf("max attempts (%d) exceeded; last: %s", s.cfg.MaxAttempts, job.LastError)); err != nil {
			return fmt.Errorf("mark failed: %w", err)
		}
		s.log.Error("purge job failed permanently",
			zap.String("jobID", job.JobID), zap.String("fileID", job.FileID),
			zap.Int("attempts", job.Attempts))
		return nil
	}

	switch job.State {
	case repository.PurgePending:
		// batch_count may not be set yet (crash between Create and
		// SetBatchCount). publishRequest sets it and publishes every batch
		// with its plain per-batch MsgId — fresh for batches that never
		// landed, dedup-swallowed for any that secretly did.
		if err := s.deps.PublishRequest(ctx, job, job.Attempts); err != nil {
			s.recordErr(ctx, job, err)
			return fmt.Errorf("republish (pending): %w", err)
		}
		if err := s.deps.UpdateState(ctx, job, repository.PurgeDecrementRequested, job.LastError); err != nil {
			return fmt.Errorf("advance pending→requested: %w", err)
		}

	case repository.PurgeDecrementRequested:
		// Re-drive via the batched publisher. With batch_count set
		// the coordinator republishes ONLY the missing batches with
		// attempt-suffixed MsgIds — the original per-batch publishes were
		// delivered at least once, so this is a genuine loss-retry. With
		// batch_count unset (a backfill-missed legacy job, or a crash between
		// the state advance and SetBatchCount) the coordinator falls back to a
		// full publish with plain per-batch MsgIds — fresh for batches that
		// never landed, dedup-swallowed for any that secretly did.
		if err := s.deps.PublishRequest(ctx, job, job.Attempts); err != nil {
			s.recordErr(ctx, job, err)
			return fmt.Errorf("republish (requested): %w", err)
		}

	case repository.PurgeDecremented:
		// Decrement landed; finish metadata removal. Idempotent.
		if err := s.deps.RemoveMetadataAndComplete(ctx, job); err != nil {
			s.recordErr(ctx, job, err)
			return fmt.Errorf("finish metadata removal: %w", err)
		}

	case repository.PurgeFailed, repository.PurgeComplete:
		// Nothing to do: FAILED stays for alerting, COMPLETE is handled by
		// the coordinator paths.

	default:
		s.log.Warn("purge sweeper: unknown state in work list",
			zap.String("jobID", job.JobID), zap.String("state", job.State))
	}
	return nil
}

// recordErr stores a dispatch failure on the job row. Best-effort: the claim
// already advanced the schedule, so the job retries whether or not this lands.
func (s *Sweeper) recordErr(ctx context.Context, job repository.PurgeJob, err error) {
	if rerr := s.deps.RecordError(ctx, job.JobID, err.Error()); rerr != nil {
		s.log.Warn("purge sweeper: record error", zap.String("jobID", job.JobID), zap.Error(rerr))
	}
}

// ComputeBackoff returns the delay before the NEXT attempt for a job that was
// just claimed: base × multiplier^(attempts-1), capped at MaxRetryDelay.
// (attempts is post-claim, so the first retry waits the base delay.)
func ComputeBackoff(job repository.PurgeJob, cfg SweeperConfig) time.Duration {
	n := job.Attempts - 1
	if n < 0 {
		n = 0
	}
	d := cfg.InitialRetryDelay
	for i := 0; i < n && d < cfg.MaxRetryDelay; i++ {
		d = time.Duration(float64(d) * cfg.BackoffMultiplier)
	}
	if d > cfg.MaxRetryDelay {
		d = cfg.MaxRetryDelay
	}
	if d < 0 {
		d = 0
	}
	return d
}

// DueBuckets returns the purge_jobs_active hour-bucket keys to scan: the
// current hour plus enough previous hours that NO job can fall out of the
// scan window while it can still legitimately retry.
//
// Sizing rule: the worst case is a job retrying every MaxRetryDelay until
// MaxAttempts, i.e. roughly MaxAttempts × MaxRetryDelay of wall clock. The
// window is that duration plus one full interval of margin, converted to
// hours. A job whose backoff keeps hitting the cap stays visible until it
// either completes or is marked FAILED — it never silently vanishes from the
// sweeper's view.
func DueBuckets(now time.Time, cfg SweeperConfig) []string {
	lookback := cfg.MaxAttempts*int(cfg.MaxRetryDelay/time.Hour) + 2 // +2h margin
	if lookback < 24 {
		lookback = 24 // always cover at least a day of buckets
	}
	hour := now.UTC().Truncate(time.Hour)
	buckets := make([]string, 0, lookback+1)
	for i := 0; i <= lookback; i++ {
		buckets = append(buckets, hour.Add(-time.Duration(i)*time.Hour).Format("2006010215"))
	}
	return buckets
}
