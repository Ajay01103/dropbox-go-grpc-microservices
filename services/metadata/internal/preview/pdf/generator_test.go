package pdf

import (
	"testing"
	"time"
)

// TestPDFEnvelope pins the pdf family's resource decisions from the plan
// (§10): 100MB source cap, 15s render timeout.
func TestPDFEnvelope(t *testing.T) {
	if maxPDFSourceBytes != 100<<20 {
		t.Fatalf("maxPDFSourceBytes = %d, want %d", maxPDFSourceBytes, 100<<20)
	}
	if pdfRenderTimeout != 15*time.Second {
		t.Fatalf("pdfRenderTimeout = %v, want 15s", pdfRenderTimeout)
	}
}
