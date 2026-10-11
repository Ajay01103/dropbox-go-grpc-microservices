package video

import (
	"testing"
	"time"
)

// TestVideoEnvelope pins the video family's resource decisions from the plan
// (§10): 500MB source cap, 20s ffmpeg timeout, frame at 10% depth.
func TestVideoEnvelope(t *testing.T) {
	if maxVideoSourceBytes != 500<<20 {
		t.Fatalf("maxVideoSourceBytes = %d, want %d", maxVideoSourceBytes, 500<<20)
	}
	if ffmpegTimeout != 20*time.Second {
		t.Fatalf("ffmpegTimeout = %v, want 20s", ffmpegTimeout)
	}
	if ffprobeTimeout >= ffmpegTimeout {
		t.Fatalf("ffprobe timeout (%v) must be shorter than the ffmpeg budget", ffprobeTimeout)
	}
	if frameSeekFraction <= 0 || frameSeekFraction >= 1 {
		t.Fatalf("frameSeekFraction = %v, want in (0,1)", frameSeekFraction)
	}
}

// TestVideoBackOff pins the steeper retry schedule (fewer, longer-spaced
// retries — external-binary failures are usually deterministic).
func TestVideoBackOff(t *testing.T) {
	if len(backOff) != 4 {
		t.Fatalf("backOff has %d tiers, want 4", len(backOff))
	}
	for i := 1; i < len(backOff); i++ {
		if backOff[i] <= backOff[i-1] {
			t.Fatalf("backOff not strictly increasing: %v", backOff)
		}
	}
}
