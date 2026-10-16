package common

import (
	"bytes"
	"image"
	"image/jpeg"
	"strings"
	"testing"
)

// TestCheckSourcePixelsIsABombGuard pins the decode-time memory guard: a header
// may declare far more pixels than the source byte cap can see, and the decoder
// allocates the whole pixel buffer before validating any content. The guard has
// to refuse it from the header alone, permanently, without ever decoding.
func TestCheckSourcePixelsIsABombGuard(t *testing.T) {
	// 20000x20000 = 400 megapixels, declared by an 18-byte file.
	bomb := jpegHeader(20000, 20000)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(bomb))
	if err != nil {
		t.Fatalf("fixture is not a parsable header: %v", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) <= maxSourcePixels {
		t.Fatalf("fixture declares %dx%d, which is under the cap and proves nothing", cfg.Width, cfg.Height)
	}
	t.Logf("fixture: %s header declaring %dx%d in %d bytes", format, cfg.Width, cfg.Height, len(bomb))

	err = CheckSourcePixels(bytes.NewReader(bomb))
	if err == nil || !strings.Contains(err.Error(), "over the") && !strings.Contains(err.Error(), "cap is") {
		t.Fatalf("bomb header: err = %v, want the size cap to reject it", err)
	}
	if !IsPermanent(err) {
		t.Fatal("an oversized header must be permanent: no retry shrinks it")
	}

	// A plausible photograph must still pass.
	var photo bytes.Buffer
	if err := jpeg.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 4000, 3000)), nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	if err := CheckSourcePixels(bytes.NewReader(photo.Bytes())); err != nil {
		t.Fatalf("12 megapixel photo rejected: %v", err)
	}

	// Undecodable bytes fail here rather than deeper in the pipeline.
	err = CheckSourcePixels(strings.NewReader("this is not an image"))
	if err == nil || !IsPermanent(err) {
		t.Fatalf("undecodable header: err = %v, want a permanent decode error", err)
	}
}

// TestCheckSourcePixelsRejectsDegenerateDimensions covers headers that parse
// but declare nothing usable.
func TestCheckSourcePixelsRejectsDegenerateDimensions(t *testing.T) {
	err := CheckSourcePixels(bytes.NewReader(jpegHeader(0, 0)))
	if err == nil || !strings.Contains(err.Error(), "invalid dimensions") {
		t.Fatalf("zero-dimension header: err = %v, want an invalid-dimensions error", err)
	}
}

// jpegHeader builds a JPEG carrying SOI, APP0 and SOF0 only — everything
// image.DecodeConfig reads, and nothing a decoder would allocate a buffer for.
// Dimensions live in SOF0, exactly as a decompression bomb's would.
func jpegHeader(width, height uint16) []byte {
	out := []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xE0, 0x00, 0x10, // APP0, length 16
		'J', 'F', 'I', 'F', 0x00,
		0x01, 0x01, // version 1.1
		0x00,       // density units
		0x00, 0x01, // x density
		0x00, 0x01, // y density
		0x00, 0x00, // no thumbnail
		0xFF, 0xC0, 0x00, 0x0B, // SOF0, length 11 (one component)
		0x08, // sample precision
		byte(height >> 8), byte(height),
		byte(width >> 8), byte(width),
		0x01,             // one component
		0x01, 0x11, 0x00, // component id, sampling, quant table
	}
	return out
}
