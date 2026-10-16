package common

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// TestFitScalesLongestEdge pins the sizing contract: the longest edge lands on
// maxEdge, the aspect ratio is preserved, and rounding keeps sub-pixel edges at
// least one pixel.
func TestFitScalesLongestEdge(t *testing.T) {
	cases := []struct {
		w, h, maxEdge, wantW, wantH int
	}{
		{w: 1024, h: 512, maxEdge: 512, wantW: 512, wantH: 256},
		{w: 512, h: 1024, maxEdge: 512, wantW: 256, wantH: 512},
		{w: 7, h: 5, maxEdge: 4, wantW: 4, wantH: 3},
		{w: 2000, h: 1, maxEdge: 512, wantW: 512, wantH: 1},
	}
	for _, tc := range cases {
		got := Fit(image.NewRGBA(image.Rect(0, 0, tc.w, tc.h)), tc.maxEdge)
		if got.Bounds().Dx() != tc.wantW || got.Bounds().Dy() != tc.wantH {
			t.Errorf("Fit(%dx%d, %d) = %v, want %dx%d",
				tc.w, tc.h, tc.maxEdge, got.Bounds(), tc.wantW, tc.wantH)
		}
	}
}

func TestFitLeavesWithinBoundsUntouched(t *testing.T) {
	var src image.Image = image.NewRGBA(image.Rect(0, 0, 100, 50))
	if got := Fit(src, 512); got != src {
		t.Fatalf("Fit returned a new %T for an in-bounds source; want the source itself", got)
	}
}

// TestFitResamplesSmoothly is the regression guard for the old per-pixel
// nearest-neighbour loop: a hard black/white edge scaled down must produce
// intermediate greys, which nearest-neighbour sampling cannot.
func TestFitResamplesSmoothly(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{A: 255}
			if x >= 32 {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}

	got := Fit(src, 8)
	smooth := 0
	for y := 0; y < got.Bounds().Dy(); y++ {
		for x := 0; x < got.Bounds().Dx(); x++ {
			r, _, _, _ := got.At(x, y).RGBA()
			// Any value that is neither pure black nor pure white can only
			// come from combining neighbouring source pixels.
			if v := int(r >> 8); v > 0 && v < 255 {
				smooth++
			}
		}
	}
	if smooth == 0 {
		t.Fatal("no interpolated pixels: Fit appears to sample nearest-neighbour again")
	}
}

// TestFlattenOnWhiteCompositesTransparencyOntoWhite pins the fix for
// transparent PNGs turning into black JPEGs.
func TestFlattenOnWhiteCompositesTransparencyOntoWhite(t *testing.T) {
	transparent := image.NewNRGBA(image.Rect(0, 0, 2, 2)) // zero value = fully transparent
	flat := FlattenOnWhite(transparent)
	r, g, b, a := flat.At(0, 0).RGBA()
	if a != 0xffff || r != 0xffff || g != 0xffff || b != 0xffff {
		t.Fatalf("transparent pixel flattened to rgba(%d,%d,%d,%d), want opaque white",
			r>>8, g>>8, b>>8, a>>8)
	}

	// Semi-transparent black must blend toward white, not stay black.
	half := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	half.Set(0, 0, color.NRGBA{A: 128})
	merged := FlattenOnWhite(half)
	hr, _, _, ha := merged.At(0, 0).RGBA()
	if ha != 0xffff {
		t.Fatalf("alpha after flatten = %d, want opaque", ha>>8)
	}
	if v := int(hr >> 8); v < 90 || v > 170 {
		t.Fatalf("semi-transparent black flattened to %d, want a mid grey", v)
	}
}

func TestFlattenOnWhiteLeavesOpaqueSourcesAlone(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var img image.Image = src
	if got := FlattenOnWhite(img); got != img {
		t.Fatalf("opaque source copied (%T) instead of returned as-is", got)
	}
}

// TestEncodeJPEGFlattensTransparency covers the whole path end to end: a fully
// transparent image encoded through EncodeJPEG must decode back to white.
func TestEncodeJPEGFlattensTransparency(t *testing.T) {
	encoded, err := EncodeJPEG(image.NewNRGBA(image.Rect(0, 0, 4, 4)))
	if err != nil {
		t.Fatalf("EncodeJPEG: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode encoded thumbnail: %v", err)
	}
	r, g, b, _ := decoded.At(2, 2).RGBA()
	for name, v := range map[string]uint32{"r": r, "g": g, "b": b} {
		if v>>8 < 200 {
			t.Fatalf("transparent source encoded as %s=%d; want near-white", name, v>>8)
		}
	}
}
