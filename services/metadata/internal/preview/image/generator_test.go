package image

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// TestSizeCapConstant pins the image family's 50 MB source cap decision.
func TestSizeCapConstant(t *testing.T) {
	if maxSourceBytes != 50<<20 {
		t.Fatalf("maxSourceBytes = %d, want %d", maxSourceBytes, 50<<20)
	}
}

// TestDecodeOrientedAppliesEXIF pins the phone-photo fix: the EXIF orientation
// tag must be applied to the decoded frame.
func TestDecodeOrientedAppliesEXIF(t *testing.T) {
	// A 4x2 "sensor frame": red left half, blue right half.
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			c := color.NRGBA{B: 255, A: 255}
			if x < 2 {
				c = color.NRGBA{R: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}

	// Orientation 6 ("the 0th row is the visual right-hand side") means the
	// frame must be rotated 90 degrees clockwise for display: the result is
	// 2x4 and the stored left (red) half becomes the top rows.
	rotated, err := decodeOriented(bytes.NewReader(exifJPEG(t, src, 6)))
	if err != nil {
		t.Fatalf("decodeOriented(orientation 6): %v", err)
	}
	if rotated.Bounds().Dx() != 2 || rotated.Bounds().Dy() != 4 {
		t.Fatalf("orientation 6 produced %v, want 2x4 (rotated)", rotated.Bounds())
	}
	if r, _, _, _ := rotated.At(0, 0).RGBA(); r>>8 < 128 {
		t.Fatal("red half did not land in the top rows: orientation not applied 90 CW")
	}
	if _, _, b, _ := rotated.At(1, 3).RGBA(); b>>8 < 128 {
		t.Fatal("blue half did not land in the bottom rows: orientation not applied 90 CW")
	}

	// Orientation 1 (upright) must leave the frame alone.
	upright, err := decodeOriented(bytes.NewReader(exifJPEG(t, src, 1)))
	if err != nil {
		t.Fatalf("decodeOriented(orientation 1): %v", err)
	}
	if upright.Bounds() != src.Bounds() {
		t.Fatalf("orientation 1 produced %v, want %v", upright.Bounds(), src.Bounds())
	}
	if r, _, _, _ := upright.At(0, 0).RGBA(); r>>8 < 128 {
		t.Fatal("orientation 1 moved the stored frame")
	}
}

// TestDecodeOrientedWithoutEXIF covers ordinary images: no EXIF segment is
// present, so the decoder must fall back to the raw frame.
func TestDecodeOrientedWithoutEXIF(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 7, 3)), nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	img, err := decodeOriented(&buf)
	if err != nil {
		t.Fatalf("decodeOriented: %v", err)
	}
	if img.Bounds().Dx() != 7 || img.Bounds().Dy() != 3 {
		t.Fatalf("plain JPEG decoded to %v, want 7x3", img.Bounds())
	}
}

// TestRenderThumbnailFlattensAndFits covers the image family's whole output
// step: a transparent 1024x512 source must come back as a 512x256 JPEG whose
// transparent pixels are white — not the black jpeg.Encode produces when it is
// handed an image with an alpha channel.
func TestRenderThumbnailFlattensAndFits(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1024, 512)) // fully transparent
	for y := 200; y < 312; y++ {
		for x := 400; x < 624; x++ {
			src.Set(x, y, color.NRGBA{R: 220, G: 30, B: 30, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}

	// Passed as a seekable reader, exactly as the worker passes its scratch
	// file: the header pass and the decode are two passes over one source.
	thumbnail, err := renderThumbnail(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("renderThumbnail: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(thumbnail))
	if err != nil {
		t.Fatalf("decode rendered thumbnail: %v", err)
	}
	if decoded.Bounds().Dx() != 512 || decoded.Bounds().Dy() != 256 {
		t.Fatalf("thumbnail is %v, want 512x256", decoded.Bounds())
	}

	// A corner was transparent in the source, so it must be white.
	r, g, b, _ := decoded.At(2, 2).RGBA()
	if r>>8 < 200 || g>>8 < 200 || b>>8 < 200 {
		t.Fatalf("transparent corner rendered as rgb(%d,%d,%d), want near-white", r>>8, g>>8, b>>8)
	}
	// The opaque red square must have survived the downscale.
	cr, cg, _, _ := decoded.At(256, 128).RGBA()
	if cr>>8 < 150 || cg>>8 > 120 {
		t.Fatalf("red square rendered as r=%d g=%d, want red", cr>>8, cg>>8)
	}
}

// exifJPEG re-wraps an encoded JPEG with a single-entry EXIF APP1 segment
// carrying the given orientation tag — the shape a phone camera writes.
func exifJPEG(t *testing.T, src image.Image, orientation uint16) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, src, nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	raw := encoded.Bytes()

	// Little-endian TIFF header plus one IFD0 entry: Orientation (0x0112),
	// SHORT, count 1, value inline.
	tiff := []byte{
		'I', 'I', 0x2A, 0x00, // byte order, magic 42
		0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
		0x01, 0x00, // one entry
		0x12, 0x01, // tag 0x0112
		0x03, 0x00, // type SHORT
		0x01, 0x00, 0x00, 0x00, // count 1
		byte(orientation), byte(orientation >> 8), 0x00, 0x00, // value, padded
		0x00, 0x00, 0x00, 0x00, // no next IFD
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)

	segmentLen := len(payload) + 2
	out := make([]byte, 0, len(raw)+len(payload)+4)
	out = append(out, raw[:2]...) // SOI
	out = append(out, 0xFF, 0xE1, byte(segmentLen>>8), byte(segmentLen))
	out = append(out, payload...)
	out = append(out, raw[2:]...)
	return out
}
