package purge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

type recordingConsolidatedDeleter struct {
	calls []string
	err   error
}

func (d *recordingConsolidatedDeleter) DeleteConsolidated(_ context.Context, fileID string) error {
	d.calls = append(d.calls, fileID)
	return d.err
}

// TestDeleteConsolidatedGatesTheSaga pins the three outcomes of the object
// deletion the purge runs before it drops a file's metadata:
//
//   - no deleter configured (Tier A-only deployment, tests): skip, don't fail.
//   - deletion failed: return the error so the job retries instead of being
//     reported COMPLETE with the file's assembled copy still in the bucket.
//     This is the leak being fixed — the consolidated key is derived from the
//     file id and its registry row is deleted in the same step, so a purge
//     that skips or swallows this failure leaves an object nothing can find
//     again.
//   - deletion succeeded: exactly one call, for the right file.
func TestDeleteConsolidatedGatesTheSaga(t *testing.T) {
	const fileID = "0f6e2f2c-6a1a-4b1e-9a3e-8d2f9f1c7b45"

	t.Run("no deleter configured", func(t *testing.T) {
		c := &Coordinator{logger: zap.NewNop()}
		if err := c.deleteConsolidated(context.Background(), fileID, "job-1"); err != nil {
			t.Fatalf("deleteConsolidated without a deleter = %v, want nil", err)
		}
	})

	t.Run("failure is returned, not swallowed", func(t *testing.T) {
		deleter := &recordingConsolidatedDeleter{err: errors.New("bucket refused")}
		c := &Coordinator{consolidatedDeleter: deleter, logger: zap.NewNop()}
		err := c.deleteConsolidated(context.Background(), fileID, "job-2")
		if err == nil {
			t.Fatal("deleteConsolidated swallowed the storage error")
		}
		if !strings.Contains(err.Error(), fileID) {
			t.Errorf("error %q does not name the file whose object survived", err)
		}
		if len(deleter.calls) != 1 || deleter.calls[0] != fileID {
			t.Errorf("deleter calls = %v, want one call for %s", deleter.calls, fileID)
		}
	})

	t.Run("success deletes the file's own object", func(t *testing.T) {
		deleter := &recordingConsolidatedDeleter{}
		c := &Coordinator{consolidatedDeleter: deleter, logger: zap.NewNop()}
		if err := c.deleteConsolidated(context.Background(), fileID, "job-3"); err != nil {
			t.Fatalf("deleteConsolidated: %v", err)
		}
		if len(deleter.calls) != 1 || deleter.calls[0] != fileID {
			t.Errorf("deleter calls = %v, want one call for %s", deleter.calls, fileID)
		}
	})
}
