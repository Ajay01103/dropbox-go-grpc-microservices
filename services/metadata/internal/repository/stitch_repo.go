package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// StitchState is the Tier B lifecycle of a file's consolidated object.
// 'stitching' is a claim marker (crash-safe retry); 'ready' means the S3
// multipart object is complete and presignable; 'failed' is permanent-for-now
// (retried by a later sweep pass with a fresh claim).
type StitchState string

const (
	// StitchStatePending is the enqueue state written at CreateFile time: the
	// file exists and a consolidated object does not yet. Any worker may claim
	// it.
	StitchStatePending   StitchState = "pending"
	StitchStateStitching StitchState = "stitching"
	StitchStateReady     StitchState = "ready"
	StitchStateFailed    StitchState = "failed"
)

// StitchPendingRef is one Tier B work item (a file awaiting consolidation).
type StitchPendingRef = StitchRecord

// stitchIndexStates are the states mirrored into the stitched_objects_by_state
// work index. 'ready' is deliberately absent: a consolidated file is finished
// work, so dropping its index row is what removes it from the sweep forever.
var stitchIndexStates = []StitchState{StitchStatePending, StitchStateStitching, StitchStateFailed}

// Enqueue records that fileID awaits Tier B consolidation and asserts its work
// index entry so the stitcher can actually find it. Idempotent: a replayed
// CreateFile never flips a 'ready' row back to 'pending' (the base INSERT only
// writes rows that do not exist yet), and an already-tracked row merely has its
// index entry re-asserted — which is how rows written before the work index
// existed get repaired.
func (r *StitchRepo) Enqueue(ctx context.Context, fileID string, now time.Time) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	existing, err := r.stateOf(ctx, id)
	switch {
	case err == nil:
		// Already tracked: re-assert the index entry for its current state
		// (indexSet ignores 'ready', which owns no index row).
		return r.indexSet(ctx, id, "", existing)
	case err != gocql.ErrNotFound:
		return err
	}
	// Index before the base row: a crash in between then leaves a dangling
	// index row, which the next sweep detects and reaps, instead of a base row
	// the sweeper would never look at. See ClaimForStitch.
	if err := r.indexSet(ctx, id, "", StitchStatePending); err != nil {
		return err
	}
	return r.session.Query(
		`INSERT INTO stitched_objects (file_id, state, claimed_at, attempts) VALUES (?, ?, ?, 0) IF NOT EXISTS`,
		id, string(StitchStatePending), now,
	).WithContext(ctx).Exec()
}

// StitchRecord is one row of the stitched_objects Tier B registry. ObjectKey
// is only meaningful in the ready state; Attempts grows across retry sweeps.
type StitchRecord struct {
	FileID      string
	State       StitchState
	ObjectKey   string
	SizeBytes   int64
	ETag        string
	Attempts    int
	ClaimedAt   time.Time
	CompletedAt time.Time
}

// StitchRepo owns the stitched_objects table. In-memory only: callers use it
// to claim work and look up the presigned target.
type StitchRepo struct {
	session *gocql.Session
}

func NewStitchRepo(session *gocql.Session) *StitchRepo {
	return &StitchRepo{session: session}
}

// ClaimForStitch atomically claims a file for stitching. Returns (record,
// claimed): claimed=false means either the work is already done ('ready') or
// another worker actively holds the claim ('stitching' younger than the
// stale window). Pre-existing 'failed' rows and stale 'stitching' claims are
// re-claimed with a fresh attempt count.
func (r *StitchRepo) ClaimForStitch(ctx context.Context, fileID string, staleClaimAfter time.Duration, now time.Time) (StitchRecord, bool, error) {
	id, err := parseID(fileID)
	if err != nil {
		return StitchRecord{}, false, fmt.Errorf("parse file id: %w", err)
	}
	current, err := r.getByID(ctx, id)
	switch {
	case err == gocql.ErrNotFound:
		// First stitch request for this file. The index entry goes in first: a
		// crash between the two writes then leaves a dangling index row (found
		// and reaped by the next sweep) rather than a 'stitching' row that no
		// sweep will ever revisit.
		rec := StitchRecord{FileID: fileID, State: StitchStateStitching, Attempts: 1, ClaimedAt: now}
		if err := r.indexSet(ctx, id, "", StitchStateStitching); err != nil {
			return StitchRecord{}, false, err
		}
		if err := r.insert(ctx, rec, now); err != nil {
			return StitchRecord{}, false, err
		}
		return rec, true, nil
	case err != nil:
		return StitchRecord{}, false, fmt.Errorf("read stitch record: %w", err)
	}
	switch current.State {
	case StitchStateReady:
		return current, false, nil
	case StitchStatePending:
		// Enqueued at CreateFile time and never claimed (or a worker's claim
		// vanished with the process): claim it now.
	case StitchStateStitching:
		if current.ClaimedAt.Add(staleClaimAfter).After(now) {
			return StitchRecord{}, false, nil // another worker is on it
		}
		// Fall through: stale claim is re-claimed below.
	case StitchStateFailed:
		// Retry with a fresh claim.
	default:
		return StitchRecord{}, false, fmt.Errorf("unknown stitch state %q", current.State)
	}
	claimed := StitchRecord{FileID: fileID, State: StitchStateStitching, Attempts: current.Attempts + 1, ClaimedAt: now}
	if err := r.updateClaim(ctx, id, claimed, now); err != nil {
		return StitchRecord{}, false, err
	}
	if err := r.indexSet(ctx, id, current.State, StitchStateStitching); err != nil {
		return StitchRecord{}, false, err
	}
	return claimed, true, nil
}

// MarkReady records the consolidated object key/etag/size. LWT-guarded so a
// purge racing the stitcher never resurrects a row for a deleted file.
func (r *StitchRepo) MarkReady(ctx context.Context, fileID, objectKey, etag string, sizeBytes int64, now time.Time) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	// The state being retired has to be known so its index entry goes with it:
	// leaving the file out of the work index is what marks it done.
	previous, err := r.stateOf(ctx, id)
	if err == gocql.ErrNotFound {
		return errors.New("stitch record vanished (file purged mid-stitch)")
	}
	if err != nil {
		return err
	}
	applied, err := r.session.Query(
		`UPDATE stitched_objects SET state = ?, object_key = ?, size_bytes = ?, etag = ?, completed_at = ?, attempts = attempts + 1 WHERE file_id = ? IF EXISTS`,
		string(StitchStateReady), objectKey, sizeBytes, etag, now, id,
	).WithContext(ctx).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("mark stitch ready: %w", err)
	}
	if !applied {
		return errors.New("stitch record vanished (file purged mid-stitch)")
	}
	return r.indexSet(ctx, id, previous, StitchStateReady)
}

// MarkFailed records a failed attempt without dropping the claim time, so the
// next sweep re-clauses cleanly rather than treating the row as stale-in-progress.
func (r *StitchRepo) MarkFailed(ctx context.Context, fileID string, now time.Time) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	previous, err := r.stateOf(ctx, id)
	if err != nil {
		return err
	}
	if err := r.session.Query(
		`UPDATE stitched_objects SET state = ?, completed_at = ? WHERE file_id = ?`,
		string(StitchStateFailed), now, id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("mark stitch failed: %w", err)
	}
	// A failed row stays in the work index on purpose: the next sweep retries
	// it until MaxAttempts retires it for good.
	return r.indexSet(ctx, id, previous, StitchStateFailed)
}

// MarkStitchingReset re-clauses a failed row (keeps attempts) after a purge
// or manual retry.
func (r *StitchRepo) MarkStitchingReset(ctx context.Context, fileID string, now time.Time) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	previous, err := r.stateOf(ctx, id)
	if err != nil {
		return err
	}
	if err := r.session.Query(
		`UPDATE stitched_objects SET state = ?, claimed_at = ? WHERE file_id = ?`,
		string(StitchStateStitching), now, id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("reset stitch claim: %w", err)
	}
	return r.indexSet(ctx, id, previous, StitchStateStitching)
}

// ListPending returns up to limit non-ready stitch candidates (a sweep work
// list). It reads the state-partitioned work index — one partition read per
// actionable state, no filtering scan — and then the authoritative base row
// for each candidate, which is where the claim time and attempt count live.
func (r *StitchRepo) ListPending(ctx context.Context, limit int) ([]StitchRecord, error) {
	var records []StitchRecord
	for _, state := range stitchIndexStates {
		if len(records) >= limit {
			break
		}
		iter := r.session.Query(
			`SELECT file_id FROM stitched_objects_by_state WHERE state = ? LIMIT ?`,
			string(state), limit,
		).WithContext(ctx).Iter()
		var ids []gocql.UUID
		var id gocql.UUID
		for iter.Scan(&id) {
			ids = append(ids, id)
		}
		if err := iter.Close(); err != nil {
			return nil, fmt.Errorf("list %s stitch candidates: %w", state, err)
		}
		for _, id := range ids {
			if len(records) >= limit {
				break
			}
			rec, err := r.getByID(ctx, id)
			switch {
			case err == gocql.ErrNotFound:
				// Dangling index row: the base row was purged, or an enqueue was
				// interrupted before its base write. Reap it so it stops taking up
				// sweep slots.
				_ = r.indexClear(ctx, state, id)
				continue
			case err != nil:
				return nil, err
			case rec.State == StitchStateReady:
				continue // index lag; never re-open finished work
			}
			records = append(records, rec)
		}
	}
	return records, nil
}

// GetStitchedObjectKey returns the consolidated object key for a 'ready'
// file, or "" when the file has no ready consolidated object (Tier A path).
func (r *StitchRepo) GetStitchedObjectKey(ctx context.Context, fileID string) (string, error) {
	id, err := parseID(fileID)
	if err != nil {
		return "", fmt.Errorf("parse file id: %w", err)
	}
	var state, objectKey string
	err = r.session.Query(
		`SELECT state, object_key FROM stitched_objects WHERE file_id = ?`,
		id,
	).WithContext(ctx).Scan(&state, &objectKey)
	if err == gocql.ErrNotFound || (err == nil && StitchState(state) != StitchStateReady) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read stitch record: %w", err)
	}
	return objectKey, nil
}

// Delete removes the stitch row and its work-index entries (purge path).
func (r *StitchRepo) Delete(ctx context.Context, fileID string) error {
	id, err := parseID(fileID)
	if err != nil {
		return fmt.Errorf("parse file id: %w", err)
	}
	if err := r.session.Query(`DELETE FROM stitched_objects WHERE file_id = ?`, id).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete stitch record: %w", err)
	}
	for _, state := range stitchIndexStates {
		if err := r.indexClear(ctx, state, id); err != nil {
			return err
		}
	}
	return nil
}

func (r *StitchRepo) insert(ctx context.Context, rec StitchRecord, now time.Time) error {
	id, err := parseID(rec.FileID)
	if err != nil {
		return err
	}
	return r.session.Query(
		`INSERT INTO stitched_objects (file_id, state, claimed_at, attempts) VALUES (?, ?, ?, 1)`,
		id, string(rec.State), now,
	).WithContext(ctx).Exec()
}

func (r *StitchRepo) updateClaim(ctx context.Context, id gocql.UUID, rec StitchRecord, now time.Time) error {
	return r.session.Query(
		`UPDATE stitched_objects SET state = ?, attempts = ?, claimed_at = ? WHERE file_id = ?`,
		string(rec.State), rec.Attempts, now, id,
	).WithContext(ctx).Exec()
}

// getByID reads one registry row, returning gocql.ErrNotFound when the file has
// no stitch row at all. The base table is the source of truth for both the
// download path and every state decision the sweeper makes.
func (r *StitchRepo) getByID(ctx context.Context, id gocql.UUID) (StitchRecord, error) {
	var state, objectKey, etag string
	var sizeBytes, attempts int64
	var claimedAt, completedAt time.Time
	if err := r.session.Query(
		`SELECT state, object_key, size_bytes, etag, attempts, claimed_at, completed_at FROM stitched_objects WHERE file_id = ?`,
		id,
	).WithContext(ctx).Scan(&state, &objectKey, &sizeBytes, &etag, &attempts, &claimedAt, &completedAt); err != nil {
		return StitchRecord{}, err
	}
	return StitchRecord{
		FileID:      id.String(),
		State:       StitchState(state),
		ObjectKey:   objectKey,
		SizeBytes:   sizeBytes,
		ETag:        etag,
		Attempts:    int(attempts),
		ClaimedAt:   claimedAt,
		CompletedAt: completedAt,
	}, nil
}

// stateOf reads just the lifecycle state of one registry row.
func (r *StitchRepo) stateOf(ctx context.Context, id gocql.UUID) (StitchState, error) {
	var state string
	if err := r.session.Query(
		`SELECT state FROM stitched_objects WHERE file_id = ?`, id,
	).WithContext(ctx).Scan(&state); err != nil {
		if err == gocql.ErrNotFound {
			return "", err
		}
		return "", fmt.Errorf("read stitch state: %w", err)
	}
	return StitchState(state), nil
}

// indexClear drops one index entry.
func (r *StitchRepo) indexClear(ctx context.Context, state StitchState, id gocql.UUID) error {
	if err := r.session.Query(
		`DELETE FROM stitched_objects_by_state WHERE state = ? AND file_id = ?`,
		string(state), id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("delete stitch index row (%s): %w", state, err)
	}
	return nil
}

// indexSet reconciles a file's work-index entry across a state transition:
// retire the entry for from and publish one for to. An empty from means "no
// previous entry"; 'ready' as to retires without replacing, which is how a
// consolidated file leaves the sweeper's work list for good. Index writes are
// best-effort in the sense that the base row always wins: a stale entry only
// costs the sweeper one claim that ClaimForStitch declines.
func (r *StitchRepo) indexSet(ctx context.Context, id gocql.UUID, from, to StitchState) error {
	if from != "" && from != to {
		if err := r.indexClear(ctx, from, id); err != nil {
			return err
		}
	}
	if to == "" || to == StitchStateReady || to == from {
		return nil
	}
	if err := r.session.Query(
		`INSERT INTO stitched_objects_by_state (state, file_id) VALUES (?, ?)`,
		string(to), id,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("index stitch state (%s): %w", to, err)
	}
	return nil
}
