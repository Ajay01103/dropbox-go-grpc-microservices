package video

import (
	"strings"
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

// TestArgsRestrictInputProtocols pins the hardening both binaries depend on:
// ffmpeg chooses its demuxer from the file's CONTENT, so an uploaded playlist
// can reference other paths or URLs even though the command line only ever
// names a scratch file. The whitelist has to be an input option, so it must
// come before the input on both command lines.
func TestArgsRestrictInputProtocols(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		inputIndex func([]string) int
		inputDescr string
	}{
		{
			name: "ffmpeg frame extraction",
			args: ffmpegFrameArgs("1.500", "input", "frame.jpg"),
			inputIndex: func(args []string) int {
				return slicesIndex(args, "-i")
			},
			inputDescr: "-i",
		},
		{
			name: "ffprobe duration",
			args: ffprobeDurationArgs("input"),
			inputIndex: func(args []string) int {
				return slicesIndex(args, "input")
			},
			inputDescr: "the trailing input path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := slicesIndex(tc.args, "-protocol_whitelist")
			if idx < 0 {
				t.Fatalf("%v is missing -protocol_whitelist", tc.args)
			}
			if idx+1 >= len(tc.args) || tc.args[idx+1] != "file" {
				t.Fatalf("whitelist must be exactly \"file\", got %v", tc.args[idx:])
			}
			if input := tc.inputIndex(tc.args); input < 0 || idx > input {
				t.Fatalf("-protocol_whitelist must precede %s (whitelist at %d, %s at %d)",
					tc.inputDescr, idx, tc.inputDescr, input)
			}
		})
	}

	// A URL on the command line must never reappear: the whole point is that
	// these vectors name scratch files only.
	for _, args := range [][]string{
		ffmpegFrameArgs("1.500", "input", "frame.jpg"),
		ffprobeDurationArgs("input"),
	} {
		for _, arg := range args {
			if strings.Contains(arg, "://") {
				t.Fatalf("argument %q looks like a URL", arg)
			}
		}
	}
}

func slicesIndex(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}
