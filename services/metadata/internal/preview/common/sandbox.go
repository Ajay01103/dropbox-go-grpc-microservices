// Subprocess sandbox — the ONE place that defines how untrusted external
// binaries (ffmpeg, ffprobe, pdftoppm) are executed by the video/pdf preview
// workers. Keeping it in a single reviewed file is the point: no copy-pasted
// isolation policy per family.
//
// Isolation layers (defense in depth, ordered by what we control here):
//  1. Process: the decode crashes in a disposable subprocess, never the
//     worker's heap.
//  2. Timeout: a hard wall-clock kill of the whole process group on ctx
//     expiry — no orphaned ffmpeg children outliving the job.
//  3. Process group: the child runs in its own group so the group kill hits
//     any helper processes it spawned.
//  4. Scratch: every job gets a unique temp dir, removed by the CALLER via
//     defer, regardless of success/failure/timeout.
//  5. Network: no namespace tricks from Go (they need privileges we don't
//     assume); in deployment the pod's network policy denies egress for
//     preview workers. ffmpeg/poppler invocations here never take URLs.
package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// SandboxedExec describes one sandboxed subprocess run.
type SandboxedExec struct {
	// Timeout is the hard wall-clock budget for the whole run.
	Timeout time.Duration
	// ScratchDir is the job's unique working dir (caller removes it).
	ScratchDir string
}

// sandboxedError marks subprocess failures caused by our own timeout kill —
// classified transient (could be a resource-contention blip) by the callers,
// while non-zero exits of the tool itself are permanent.
type sandboxedError struct {
	timeout bool
	err     error
}

func (e *sandboxedError) Error() string { return e.err.Error() }
func (e *sandboxedError) Unwrap() error { return e.err }

// IsTimeout reports whether err came from the sandbox's timeout kill.
func IsTimeout(err error) bool {
	var se *sandboxedError
	return errors.As(err, &se) && se.timeout
}

// Run executes name with args inside the sandbox and returns stdout.
// Stderr is captured into the error for diagnosis (bounded).
func (s SandboxedExec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Timeout <= 0 {
		return nil, errors.New("sandbox: timeout must be set")
	}
	runCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, name, args...)
	if s.ScratchDir != "" {
		cmd.Dir = s.ScratchDir
	}
	applySandboxAttrs(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil } // group kill on ctx expiry
	cmd.WaitDelay = 5 * time.Second                         // belt: reap even if Cancel hangs
	cmd.Env = sandboxEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Start()
	if err != nil {
		return nil, &sandboxedError{err: fmt.Errorf("start %s: %w", name, err)}
	}
	waitErr := cmd.Wait()
	if runCtx.Err() == context.DeadlineExceeded && waitErr != nil {
		return nil, &sandboxedError{timeout: true, err: fmt.Errorf("%s exceeded %v timeout", name, s.Timeout)}
	}
	if waitErr != nil {
		return nil, &sandboxedError{err: fmt.Errorf("%s failed: %w: %s", name, waitErr, boundStderr(stderr.Bytes()))}
	}
	return stdout.Bytes(), nil
}

// ScratchDir creates a unique per-job scratch directory under the OS temp
// root. The caller owns removal (defer os.RemoveAll(dir)).
func ScratchDir(prefix string) (string, error) {
	return os.MkdirTemp("", "preview-"+prefix+"-")
}

func boundStderr(b []byte) string {
	const max = 4 << 10
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}

// sandboxEnv gives the child a minimal environment: no credentials, no proxy
// settings, nothing an attacker could leverage from the worker's env.
func sandboxEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
		"HOME=" + os.TempDir(),
	}
}
