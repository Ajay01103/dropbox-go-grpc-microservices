package repository

// TestClaimBlockDecrement_BugCondition — Property 1: Bug Condition
// LWT Scan Handles Variable Column Count
//
// Validates: Requirements 1.1, 1.2, 1.3
//
// These tests MUST FAIL on unfixed code — failure confirms the bug exists.
// They encode the EXPECTED (correct) behavior and will pass once the fix is
// applied (task 3).
//
// Bug path 1 (INSERT LWT not-applied):
//   ScyllaDB returns applied=false + 9 data columns (10 columns total).
//   Unfixed code: .Scan(&applied) → "gocql: not enough columns to scan into: have 1 want 10"
//   Fixed code:   .MapScanCAS(casMap) → applied=false, err=nil → WasReplay=true, Claimed=false
//
// Bug path 2 (UPDATE LWT not-applied):
//   ScyllaDB returns applied=false + updated_at + job_id + occurrence (4 columns total).
//   Unfixed code: .Scan(&tookOver) → "gocql: not enough columns to scan into: have 1 want 4"
//   Fixed code:   .MapScanCAS(takeoverMap) → tookOver=false, err=nil → WasReplay=true, Claimed=false

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

// ---- mock infrastructure -----------------------------------------------

// mockSession routes queries to per-call handlers provided by the test.
type mockSession struct {
	// callIndex tracks which invocation of Query() we are on.
	callIndex int
	// handlers is a list of mockQuery factories, one per Query() call in order.
	handlers []func() *mockQuery
}

func (s *mockSession) Query(_ string, _ ...interface{}) gocqlQuery {
	if s.callIndex >= len(s.handlers) {
		panic(fmt.Sprintf("mockSession: unexpected Query call #%d (only %d handlers registered)", s.callIndex, len(s.handlers)))
	}
	q := s.handlers[s.callIndex]()
	s.callIndex++
	return q
}

// mockQuery is a single-use gocqlQuery stub. Each method delegates to the
// configured func field (or no-ops if nil).
type mockQuery struct {
	scanFn       func(dest ...interface{}) error
	scanCASFn    func(dest ...interface{}) (bool, error)
	mapScanCASFn func(dest map[string]interface{}) (bool, error)
	execFn       func() error
	iterFn       func() *gocql.Iter
}

func (m *mockQuery) WithContext(_ context.Context) gocqlQuery { return m }

func (m *mockQuery) Scan(dest ...interface{}) error {
	if m.scanFn != nil {
		return m.scanFn(dest...)
	}
	return nil
}

func (m *mockQuery) ScanCAS(dest ...interface{}) (bool, error) {
	if m.scanCASFn != nil {
		return m.scanCASFn(dest...)
	}
	return false, nil
}

func (m *mockQuery) MapScanCAS(dest map[string]interface{}) (bool, error) {
	if m.mapScanCASFn != nil {
		return m.mapScanCASFn(dest)
	}
	return false, nil
}

func (m *mockQuery) Exec() error {
	if m.execFn != nil {
		return m.execFn()
	}
	return nil
}

func (m *mockQuery) Iter() *gocql.Iter {
	if m.iterFn != nil {
		return m.iterFn()
	}
	return nil
}

// ---- helpers ---------------------------------------------------------------

// lwtInsertNotApplied returns a mockQuery whose Scan() returns the error that
// gocql produces when an INSERT LWT returns applied=false with all 10 columns.
// On unfixed code ClaimBlockDecrement calls .Scan(&applied) here — this error
// causes ClaimBlockDecrement to return a wrapped error, making the test fail.
func lwtInsertNotApplied() *mockQuery {
	return &mockQuery{
		scanFn: func(_ ...interface{}) error {
			return fmt.Errorf("gocql: not enough columns to scan into: have 1 want 10")
		},
		mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
			// Fixed code path: return applied=false, no error.
			dest["[applied]"] = false
			return false, nil
		},
	}
}

// lwtInsertNotApplied1Col simulates the INSERT LWT returning applied=false
// with a 1-column result (scan succeeds, sets applied=false). This is used in
// the UPDATE LWT bug test to bypass the INSERT LWT and isolate the UPDATE path.
// (In a real ScyllaDB the INSERT-not-applied always returns 10 columns — this
// mock simplifies the setup so the UPDATE LWT counterexample can be surfaced
// independently.)
func lwtInsertNotApplied1Col() *mockQuery {
	return &mockQuery{
		scanFn: func(dest ...interface{}) error {
			if len(dest) == 1 {
				*(dest[0].(*bool)) = false
			}
			return nil
		},
		mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
			return false, nil
		},
	}
}

// selectExistingRow returns a mockQuery whose Scan() populates the operation
// fields (the follow-up SELECT after INSERT not-applied). This is needed so
// the test can reach the UPDATE LWT on the stale-takeover path.
//
// jobID and occurrence are unused — the SELECT is keyed by (job_id,
// occurrence), which the mock ignores — but they are kept in the signature
// to document what row is being simulated.
func selectExistingRow(updatedAt time.Time) *mockQuery {
	return &mockQuery{
		scanFn: func(dest ...interface{}) error {
			// dest order matches the SELECT in ClaimBlockDecrement:
			// &operation.BlockHash, &operation.Status, &operation.NewRefCount,
			// &operation.BecameGCCandidate, &operation.Error, &operation.ClaimedAt, &operation.UpdatedAt
			if len(dest) != 7 {
				return fmt.Errorf("selectExistingRow: unexpected dest length %d", len(dest))
			}
			*(dest[0].(*string)) = "abc123"
			*(dest[1].(*string)) = BlockDecrementClaimed
			*(dest[2].(*int64)) = 0
			*(dest[3].(*bool)) = false
			*(dest[4].(*string)) = ""
			*(dest[5].(*time.Time)) = updatedAt
			*(dest[6].(*time.Time)) = updatedAt
			return nil
		},
	}
}

// lwtUpdateNotApplied returns a mockQuery whose Scan() returns the error that
// gocql produces when an UPDATE LWT returns applied=false with 4 columns.
// On unfixed code ClaimBlockDecrement calls .Scan(&tookOver) here — this
// error causes ClaimBlockDecrement to return a wrapped error, making the test fail.
func lwtUpdateNotApplied() *mockQuery {
	return &mockQuery{
		scanFn: func(_ ...interface{}) error {
			return fmt.Errorf("gocql: not enough columns to scan into: have 1 want 4")
		},
		mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
			// Fixed code path: return tookOver=false, no error.
			dest["[applied]"] = false
			return false, nil
		},
	}
}

// ---- bug-condition tests ---------------------------------------------------

// TestClaimBlockDecrement_BugCondition_InsertLWTNotApplied confirms that when
// the INSERT LWT returns applied=false (the row already exists), ClaimBlockDecrement
// handles the multi-column result without error and returns WasReplay=true.
//
// EXPECTED ON UNFIXED CODE: FAIL — the test assertion err==nil fails because
// ClaimBlockDecrement returns "claim decrement operation: gocql: not enough
// columns to scan into: have 1 want 10".
//
// EXPECTED ON FIXED CODE: PASS.
//
// Counterexample documented:
//
//	ClaimBlockDecrement(ctx, validJobID, 0, "somehash", staleAfter) returned
//	error: "claim decrement operation: gocql: not enough columns to scan into:
//	have 1 want 10" — proves that Scan(&applied) cannot handle the 10-column
//	LWT result set produced by ScyllaDB when the INSERT row already exists.
func TestClaimBlockDecrement_BugCondition_InsertLWTNotApplied(t *testing.T) {
	jobID := gocql.TimeUUID().String()
	staleAfter := time.Hour // long stale period — not relevant for this path

	// The INSERT LWT returns a 10-column not-applied result.
	// Unfixed code: .Scan(&applied) errors; fixed code: .MapScanCAS(casMap) succeeds.
	// After not-applied INSERT the code runs a SELECT then checks staleness.
	// We also need to serve the follow-up SELECT so execution reaches its end.
	sess := &mockSession{
		handlers: []func() *mockQuery{
			// Call 1: INSERT LWT — not applied, row already exists.
			func() *mockQuery { return lwtInsertNotApplied() },
			// Call 2: SELECT to read the existing row.
			// (only reached by fixed code — unfixed code errors before this)
			func() *mockQuery {
				// row is fresh (updated 1 second ago), so stale check won't fire
				return selectExistingRow(time.Now().UTC().Add(-1 * time.Second))
			},
		},
	}

	repo := &BlockRepo{session: sess}
	claim, err := repo.ClaimBlockDecrement(context.Background(), jobID, 0, "somehash", staleAfter)

	// On UNFIXED code this assertion fails with:
	// "expected no error, got: claim decrement operation: gocql: not enough
	//  columns to scan into: have 1 want 10"
	if err != nil {
		t.Fatalf("expected no error from ClaimBlockDecrement (INSERT LWT not-applied), got: %v\n"+
			"\nCounterexample: ClaimBlockDecrement returned error containing %q"+
			"\nThis confirms the bug: Scan(&applied) cannot handle the 10-column LWT result set.",
			err, "not enough columns")
	}
	if !claim.WasReplay {
		t.Errorf("expected WasReplay=true for INSERT-not-applied path, got %+v", claim)
	}
	if claim.Claimed {
		t.Errorf("expected Claimed=false for INSERT-not-applied path, got %+v", claim)
	}
}

// TestClaimBlockDecrement_BugCondition_UpdateLWTNotApplied confirms that when
// the UPDATE LWT (stale takeover attempt) returns applied=false, ClaimBlockDecrement
// handles the 4-column result without error and returns WasReplay=true.
//
// EXPECTED ON UNFIXED CODE: FAIL — the test assertion err==nil fails because
// ClaimBlockDecrement returns "resume stale decrement operation: gocql: not
// enough columns to scan into: have 1 want 4".
//
// EXPECTED ON FIXED CODE: PASS.
//
// Counterexample documented:
//
//	ClaimBlockDecrement(ctx, validJobID, 0, "somehash", 0) returned
//	error: "resume stale decrement operation: gocql: not enough columns to
//	scan into: have 1 want 4" — proves that Scan(&tookOver) cannot handle
//	the 4-column LWT result set (applied + updated_at + job_id + occurrence)
//	produced by ScyllaDB when the UPDATE condition fails.
func TestClaimBlockDecrement_BugCondition_UpdateLWTNotApplied(t *testing.T) {
	jobID := gocql.TimeUUID().String()
	staleAfter := time.Duration(0) // zero stale period — any existing CLAIMED row triggers takeover

	// Make the row appear stale (updated a long time ago) so the takeover LWT fires.
	staleUpdatedAt := time.Now().UTC().Add(-24 * time.Hour)

	sess := &mockSession{
		handlers: []func() *mockQuery{
			// Call 1: INSERT LWT — not applied, using 1-col mock to bypass the
			// INSERT-Scan bug and let execution reach the UPDATE LWT path.
			// (The INSERT 10-col bug is covered by the prior test.)
			func() *mockQuery { return lwtInsertNotApplied1Col() },
			// Call 2: SELECT existing row — row is CLAIMED and stale.
			func() *mockQuery {
				return selectExistingRow(staleUpdatedAt)
			},
			// Call 3: UPDATE LWT (takeover attempt) — not applied; ScyllaDB returns
			// 4 columns (applied, updated_at, job_id, occurrence).
			func() *mockQuery { return lwtUpdateNotApplied() },
			// Call 4: re-read after failed takeover to get the fresh row state.
			// The rewritten ClaimBlockDecrement always re-reads when tookOver=false
			// so it can detect a terminal status written by the winning worker.
			func() *mockQuery {
				return selectExistingRow(staleUpdatedAt)
			},
		},
	}

	repo := &BlockRepo{session: sess}
	claim, err := repo.ClaimBlockDecrement(context.Background(), jobID, 0, "somehash", staleAfter)

	// On UNFIXED code this assertion fails with:
	// "expected no error, got: resume stale decrement operation: gocql: not enough
	//  columns to scan into: have 1 want 4"
	if err != nil {
		t.Fatalf("expected no error from ClaimBlockDecrement (UPDATE LWT not-applied), got: %v\n"+
			"\nCounterexample: ClaimBlockDecrement returned error containing %q"+
			"\nThis confirms the bug: Scan(&tookOver) cannot handle the 4-column LWT result set.",
			err, "not enough columns")
	}
	if !claim.WasReplay {
		t.Errorf("expected WasReplay=true for UPDATE-not-applied path, got %+v", claim)
	}
	if claim.Claimed {
		t.Errorf("expected Claimed=false for UPDATE-not-applied path, got %+v", claim)
	}
}

// TestClaimBlockDecrement_BugCondition_ErrorShape documents the exact error
// strings produced by the bug so callers can understand what they receive.
// These are informational sub-tests that extract and display the counterexamples.
func TestClaimBlockDecrement_BugCondition_ErrorShape(t *testing.T) {
	t.Run("INSERT_LWT_error_contains_not_enough_columns", func(t *testing.T) {
		jobID := gocql.TimeUUID().String()
		sess := &mockSession{
			handlers: []func() *mockQuery{
				// Call 1: INSERT LWT — not applied.
				func() *mockQuery { return lwtInsertNotApplied() },
				// Call 2: SELECT existing row (reached by fixed code after INSERT not-applied).
				func() *mockQuery {
					return selectExistingRow(time.Now().UTC().Add(-1 * time.Second))
				},
			},
		}
		repo := &BlockRepo{session: sess}
		_, err := repo.ClaimBlockDecrement(context.Background(), jobID, 0, "somehash", time.Hour)
		// On unfixed code: err != nil and contains "not enough columns"
		// On fixed code: err == nil
		if err != nil && !strings.Contains(err.Error(), "not enough columns") {
			t.Errorf("unexpected error shape: %v", err)
		}
		if err != nil {
			t.Logf("COUNTEREXAMPLE (INSERT LWT bug): %v", err)
		}
	})

	t.Run("UPDATE_LWT_error_contains_not_enough_columns", func(t *testing.T) {
		jobID := gocql.TimeUUID().String()
		staleUpdatedAt := time.Now().UTC().Add(-24 * time.Hour)
		sess := &mockSession{
			handlers: []func() *mockQuery{
				func() *mockQuery { return lwtInsertNotApplied1Col() },
				func() *mockQuery { return selectExistingRow(staleUpdatedAt) },
				func() *mockQuery { return lwtUpdateNotApplied() },
				// Call 4: re-read after failed takeover (added by the improved
				// ClaimBlockDecrement which always re-reads to detect terminal status).
				func() *mockQuery { return selectExistingRow(staleUpdatedAt) },
			},
		}
		repo := &BlockRepo{session: sess}
		_, err := repo.ClaimBlockDecrement(context.Background(), jobID, 0, "somehash", 0)
		if err != nil && !strings.Contains(err.Error(), "not enough columns") {
			t.Errorf("unexpected error shape: %v", err)
		}
		if err != nil {
			t.Logf("COUNTEREXAMPLE (UPDATE LWT bug): %v", err)
		}
	})
}

// ---- preservation property tests ------------------------------------------

// TestClaimBlockDecrement_Preservation — Property 2: Preservation
// Applied LWT and Post-Scan Logic Unchanged
//
// Validates: Requirements 3.1, 3.2, 3.3, 3.4, 3.5, 3.6
//
// These tests MUST PASS on UNFIXED code. They encode baseline behavior for
// the non-buggy paths (INSERT applied, input validation) so we can confirm no
// regressions after the fix is applied.

// sessionCallCounter wraps mockSession and records how many times Query was called.
type sessionCallCounter struct {
	mock  *mockSession
	calls int
}

func (s *sessionCallCounter) Query(stmt string, values ...interface{}) gocqlQuery {
	s.calls++
	return s.mock.Query(stmt, values...)
}

// lwtInsertApplied returns a mockQuery whose Scan() sets applied=true (1-column
// result), simulating a fresh INSERT LWT that succeeded.
func lwtInsertApplied() *mockQuery {
	return &mockQuery{
		scanFn: func(dest ...interface{}) error {
			if len(dest) == 1 {
				*(dest[0].(*bool)) = true
			}
			return nil
		},
		mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
			// Fixed-code path: applied=true, empty map.
			return true, nil
		},
	}
}

// TestClaimBlockDecrement_Preservation_FreshClaim verifies that when the INSERT
// LWT is applied (fresh claim, 1-column result), ClaimBlockDecrement returns
// Claimed=true with the correct Operation fields and does NOT return an error.
//
// Property: For all valid (jobID, occurrence, blockHash) inputs where the
// INSERT LWT mock returns applied=true, the result is Claimed=true and the
// returned operation fields match the inserted values.
//
// EXPECTED ON UNFIXED CODE: PASS — the 1-column applied=true path is not
// affected by the column-count bug.
// EXPECTED ON FIXED CODE:   PASS — same behavior preserved.
func TestClaimBlockDecrement_Preservation_FreshClaim(t *testing.T) {
	cases := []struct {
		name       string
		jobID      string
		occurrence int
		blockHash  string
	}{
		{
			name:       "typical fresh claim",
			jobID:      gocql.TimeUUID().String(),
			occurrence: 0,
			blockHash:  "abc123def456",
		},
		{
			name:       "high occurrence value",
			jobID:      gocql.TimeUUID().String(),
			occurrence: 999,
			blockHash:  "fedcba987654",
		},
		{
			name:       "occurrence zero with long hash",
			jobID:      gocql.TimeUUID().String(),
			occurrence: 0,
			blockHash:  strings.Repeat("a", 64),
		},
		{
			name:       "occurrence one with short hash",
			jobID:      gocql.TimeUUID().String(),
			occurrence: 1,
			blockHash:  "deadbeef",
		},
		{
			name:       "max-ish occurrence",
			jobID:      gocql.TimeUUID().String(),
			occurrence: 1<<20 - 1,
			blockHash:  "cafebabe",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sess := &mockSession{
				handlers: []func() *mockQuery{
					// Call 1: INSERT LWT — applied=true (fresh row). No by-hash
					// dual-write: that table was dropped in B4.
					func() *mockQuery { return lwtInsertApplied() },
				},
			}
			repo := &BlockRepo{session: sess}
			claim, err := repo.ClaimBlockDecrement(context.Background(), tc.jobID, tc.occurrence, tc.blockHash, time.Hour)

			if err != nil {
				t.Fatalf("[%s] expected no error for fresh claim, got: %v", tc.name, err)
			}
			if !claim.Claimed {
				t.Errorf("[%s] expected Claimed=true for INSERT-applied path, got %+v", tc.name, claim)
			}
			if claim.WasReplay {
				t.Errorf("[%s] expected WasReplay=false for fresh claim, got %+v", tc.name, claim)
			}
			if claim.Operation.BlockHash != tc.blockHash {
				t.Errorf("[%s] expected Operation.BlockHash=%q, got %q", tc.name, tc.blockHash, claim.Operation.BlockHash)
			}
			if claim.Operation.Occurrence != tc.occurrence {
				t.Errorf("[%s] expected Operation.Occurrence=%d, got %d", tc.name, tc.occurrence, claim.Operation.Occurrence)
			}
			if claim.Operation.JobID != tc.jobID {
				t.Errorf("[%s] expected Operation.JobID=%q, got %q", tc.name, tc.jobID, claim.Operation.JobID)
			}
			if claim.Operation.Status != BlockDecrementClaimed {
				t.Errorf("[%s] expected Operation.Status=%q, got %q", tc.name, BlockDecrementClaimed, claim.Operation.Status)
			}
			if claim.Operation.ClaimedAt.IsZero() {
				t.Errorf("[%s] expected non-zero ClaimedAt, got zero", tc.name)
			}
			if claim.Operation.UpdatedAt.IsZero() {
				t.Errorf("[%s] expected non-zero UpdatedAt, got zero", tc.name)
			}
			// Only the INSERT LWT should have run: the by-hash dual-write is
			// gone (table dropped in B4).
			if sess.callIndex != 1 {
				t.Errorf("[%s] expected exactly 1 session call, got %d", tc.name, sess.callIndex)
			}
		})
	}
}

// TestClaimBlockDecrement_Preservation_FreshClaim_Property exercises a wide
// range of (occurrence, blockHash) combinations via a table-driven property
// sweep, verifying that the Claimed=true, WasReplay=false invariant holds for
// every valid input on the INSERT-applied path.
//
// Property: For all valid (jobID, occurrence, blockHash) inputs where the
// INSERT LWT mock returns applied=true, the result is Claimed=true and the
// returned operation fields match the inserted values.
//
// Validates: Requirements 3.1, 3.6
func TestClaimBlockDecrement_Preservation_FreshClaim_Property(t *testing.T) {
	// Generate a range of (occurrence, blockHash) pairs covering boundary and
	// mid-range values without relying on external PBT libraries.
	type input struct {
		occurrence int
		blockHash  string
	}

	var inputs []input
	for _, occ := range []int{0, 1, 2, 10, 100, 1000, 1<<15 - 1} {
		for _, hash := range []string{"a", "ab", "abcdef", strings.Repeat("x", 32), strings.Repeat("z", 64)} {
			inputs = append(inputs, input{occ, hash})
		}
	}

	for _, in := range inputs {
		in := in
		jobID := gocql.TimeUUID().String()
		sess := &mockSession{
			handlers: []func() *mockQuery{
				func() *mockQuery { return lwtInsertApplied() },
			},
		}
		repo := &BlockRepo{session: sess}
		claim, err := repo.ClaimBlockDecrement(context.Background(), jobID, in.occurrence, in.blockHash, time.Hour)
		if err != nil {
			t.Errorf("occurrence=%d blockHash=%q: expected no error, got: %v", in.occurrence, in.blockHash, err)
			continue
		}
		if !claim.Claimed {
			t.Errorf("occurrence=%d blockHash=%q: expected Claimed=true, got %+v", in.occurrence, in.blockHash, claim)
		}
		if claim.WasReplay {
			t.Errorf("occurrence=%d blockHash=%q: expected WasReplay=false, got %+v", in.occurrence, in.blockHash, claim)
		}
		if claim.Operation.BlockHash != in.blockHash {
			t.Errorf("occurrence=%d blockHash=%q: field mismatch BlockHash=%q", in.occurrence, in.blockHash, claim.Operation.BlockHash)
		}
		if claim.Operation.Occurrence != in.occurrence {
			t.Errorf("occurrence=%d blockHash=%q: field mismatch Occurrence=%d", in.occurrence, in.blockHash, claim.Operation.Occurrence)
		}
	}
}

// TestClaimBlockDecrement_Preservation_InvalidUUID verifies that when an
// invalid UUID string is provided, ClaimBlockDecrement returns a parse error
// with the "parse decrement job id" prefix and never calls the session.
//
// Property: For all invalid UUID strings (any string that fails gocql.ParseUUID),
// the function returns an error and never calls the session.
//
// Validates: Requirements 3.4
func TestClaimBlockDecrement_Preservation_InvalidUUID(t *testing.T) {
	invalidUUIDs := []string{
		"",
		"not-a-uuid",
		"00000000-0000-0000-0000-00000000000Z",  // invalid hex char
		"xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",  // invalid hex chars
		"12345678-1234-1234-1234-12345678901",   // one char too short
		"12345678-1234-1234-1234-1234567890123", // one char too long
		"   ",
		"null",
		"\x00",
		// NOTE: "00000000000000000000000000000000" (32 hex, no hyphens) IS accepted
		// by gocql.ParseUUID — it is a valid UUID representation, so it is NOT here.
	}

	for _, badID := range invalidUUIDs {
		badID := badID
		t.Run(fmt.Sprintf("uuid=%q", badID), func(t *testing.T) {
			// Counter wraps a zero-handler mockSession — any Query call panics,
			// proving the session is never reached.
			counter := &sessionCallCounter{
				mock: &mockSession{handlers: nil},
			}
			repo := &BlockRepo{session: counter}
			_, err := repo.ClaimBlockDecrement(context.Background(), badID, 0, "somehash", time.Hour)

			if err == nil {
				t.Fatalf("uuid=%q: expected parse error, got nil", badID)
			}
			if !strings.Contains(err.Error(), "parse decrement job id") {
				t.Errorf("uuid=%q: expected error with prefix %q, got: %v", badID, "parse decrement job id", err)
			}
			if counter.calls != 0 {
				t.Errorf("uuid=%q: expected 0 session calls, got %d", badID, counter.calls)
			}
		})
	}
}

// TestClaimBlockDecrement_Preservation_ValidationError verifies that when
// blockHash is empty or occurrence is negative, the function returns a
// validation error ("occurrence and block hash are required") and never calls
// the session.
//
// Property: For any blockHash="" or occurrence<0, the function returns a
// validation error and never calls the session.
//
// Validates: Requirements 3.5
func TestClaimBlockDecrement_Preservation_ValidationError(t *testing.T) {
	validJobID := gocql.TimeUUID().String()

	cases := []struct {
		name       string
		jobID      string
		occurrence int
		blockHash  string
	}{
		{name: "empty blockHash", jobID: validJobID, occurrence: 0, blockHash: ""},
		{name: "negative occurrence", jobID: validJobID, occurrence: -1, blockHash: "somehash"},
		{name: "negative occurrence large", jobID: validJobID, occurrence: -9999, blockHash: "somehash"},
		{name: "negative occurrence and empty hash", jobID: validJobID, occurrence: -1, blockHash: ""},
		{name: "occurrence negative one", jobID: validJobID, occurrence: -1, blockHash: "abc"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			counter := &sessionCallCounter{
				mock: &mockSession{handlers: nil},
			}
			repo := &BlockRepo{session: counter}
			_, err := repo.ClaimBlockDecrement(context.Background(), tc.jobID, tc.occurrence, tc.blockHash, time.Hour)

			if err == nil {
				t.Fatalf("[%s] expected validation error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "occurrence and block hash are required") {
				t.Errorf("[%s] expected error %q, got: %v", tc.name, "occurrence and block hash are required", err)
			}
			if counter.calls != 0 {
				t.Errorf("[%s] expected 0 session calls, got %d", tc.name, counter.calls)
			}
		})
	}
}

// TestClaimBlockDecrement_Preservation_ValidationError_Property sweeps a wide
// range of (occurrence, blockHash) pairs that should all trigger the validation
// guard, verifying the invariant holds across the entire invalid-input space.
//
// Validates: Requirements 3.5
func TestClaimBlockDecrement_Preservation_ValidationError_Property(t *testing.T) {
	validJobID := gocql.TimeUUID().String()

	type input struct {
		occurrence int
		blockHash  string
	}

	// All combinations of negative occurrences (including extreme values)
	// and both empty and non-empty hashes.
	invalidInputs := []input{
		{occurrence: -1, blockHash: ""},
		{occurrence: -1, blockHash: "hash"},
		{occurrence: -100, blockHash: ""},
		{occurrence: -100, blockHash: "hash"},
		{occurrence: -(1 << 30), blockHash: ""},
		{occurrence: -(1 << 30), blockHash: "hash"},
		// Empty hash with valid occurrence.
		{occurrence: 0, blockHash: ""},
		{occurrence: 1, blockHash: ""},
		{occurrence: 999, blockHash: ""},
	}

	for _, in := range invalidInputs {
		in := in
		counter := &sessionCallCounter{mock: &mockSession{handlers: nil}}
		repo := &BlockRepo{session: counter}
		_, err := repo.ClaimBlockDecrement(context.Background(), validJobID, in.occurrence, in.blockHash, time.Hour)
		if err == nil {
			t.Errorf("occurrence=%d blockHash=%q: expected validation error, got nil", in.occurrence, in.blockHash)
			continue
		}
		if !strings.Contains(err.Error(), "occurrence and block hash are required") {
			t.Errorf("occurrence=%d blockHash=%q: expected %q in error, got: %v",
				in.occurrence, in.blockHash, "occurrence and block hash are required", err)
		}
		if counter.calls != 0 {
			t.Errorf("occurrence=%d blockHash=%q: expected 0 session calls, got %d",
				in.occurrence, in.blockHash, counter.calls)
		}
	}
}
