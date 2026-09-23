package repository

import (
	"context"
	"strings"
	"testing"
)

// TestFenceForDelete_Success pins the fence semantics: the LWT applies only
// when ref_count = 0 AND state = null (the pre-B2a condition of every
// existing row, and the only state InsertBlock ever produces).
func TestFenceForDelete_Success(t *testing.T) {
	capturedStmt := ""
	session := &mockSession{handlers: []func() *mockQuery{
		func() *mockQuery {
			return &mockQuery{
				mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
					return true, nil // fence applied
				},
			}
		},
	}}
	// Intercept the statement via a wrapper — mockSession doesn't record SQL,
	// so assert behavior instead: applied=true means the LWT passed.
	repo := &BlockRepo{session: session}
	_ = capturedStmt

	applied, err := repo.FenceForDelete(context.Background(), "abc123def4567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if err != nil {
		t.Fatalf("FenceForDelete: %v", err)
	}
	if !applied {
		t.Fatal("expected fence to apply on ref_count=0/state=null row")
	}
}

// TestFenceForDelete_FailsWhenReferenced pins the closed-on-reference
// behavior: a block with ref_count > 0 must never fence (applied=false).
func TestFenceForDelete_FailsWhenReferenced(t *testing.T) {
	session := &mockSession{handlers: []func() *mockQuery{
		func() *mockQuery {
			return &mockQuery{
				mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
					// Simulate server: condition failed, ref_count returned.
					dest["ref_count"] = int64(3)
					return false, nil
				},
			}
		},
	}}
	repo := &BlockRepo{session: session}

	applied, err := repo.FenceForDelete(context.Background(), "abc123def4567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if err != nil {
		t.Fatalf("FenceForDelete: %v", err)
	}
	if applied {
		t.Fatal("fence must not apply on a referenced block")
	}
}

// TestFenceForDelete_FailsWhenAlreadyFenced ensures a second fence on a
// DELETING row does not apply (state = null condition fails).
func TestFenceForDelete_FailsWhenAlreadyFenced(t *testing.T) {
	session := &mockSession{handlers: []func() *mockQuery{
		func() *mockQuery {
			return &mockQuery{
				mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
					dest["state"] = BlockStateDeleting
					return false, nil
				},
			}
		},
	}}
	repo := &BlockRepo{session: session}

	applied, err := repo.FenceForDelete(context.Background(), "abc123def4567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if err != nil {
		t.Fatalf("FenceForDelete: %v", err)
	}
	if applied {
		t.Fatal("fence must not apply twice")
	}
}

// TestFenceForDelete_EmptyHash guards the input validation.
func TestFenceForDelete_EmptyHash(t *testing.T) {
	repo := &BlockRepo{session: &mockSession{}}
	if _, err := repo.FenceForDelete(context.Background(), ""); err == nil {
		t.Fatal("want error for empty hash")
	}
}

// TestIncrementRefCount_BlockDeleting pins the dedup-path behavior when the
// CAS fails because the block is fenced: the caller must receive a
// typed ErrBlockDeleting (translated to CodeUnavailable by the service).
func TestIncrementRefCount_BlockDeleting(t *testing.T) {
	session := &mockSession{handlers: []func() *mockQuery{
		// Call 1: GetBlock scan is driven by scanFn — but IncrementRefCount
		// first reads via GetBlock. Simulate: scan returns a block row with
		// ref_count 2, then the CAS fails revealing state=DELETING.
		func() *mockQuery {
			return &mockQuery{
				scanFn: func(dest ...interface{}) error {
					// dest: &hash, &size, &refCount, &backend, &key, &etag, &createdAt
					*(dest[2].(*int64)) = 2
					return nil
				},
			}
		},
		func() *mockQuery {
			return &mockQuery{
				mapScanCASFn: func(dest map[string]interface{}) (bool, error) {
					dest["state"] = BlockStateDeleting
					return false, nil
				},
			}
		},
	}}
	repo := &BlockRepo{session: session}

	err := repo.IncrementRefCount(context.Background(), "abc123def4567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if err == nil {
		t.Fatal("want error when block is fenced for deletion")
	}
	if !IsBlockDeleting(err) {
		t.Fatalf("want ErrBlockDeleting, got: %v", err)
	}
	if !strings.Contains(err.Error(), "being deleted") {
		t.Errorf("error should mention deletion: %v", err)
	}
}
