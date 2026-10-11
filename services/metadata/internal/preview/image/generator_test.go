package image

import "testing"

// TestSizeCapConstant pins the image family's 50 MB source cap decision.
func TestSizeCapConstant(t *testing.T) {
	if maxSourceBytes != 50<<20 {
		t.Fatalf("maxSourceBytes = %d, want %d", maxSourceBytes, 50<<20)
	}
}
