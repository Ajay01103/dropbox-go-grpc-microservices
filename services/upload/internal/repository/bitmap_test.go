package repository

import "testing"

func TestBitmapBits(t *testing.T) {
	bitmap := SetBitmapBit(nil, 0)
	bitmap = SetBitmapBit(bitmap, 9)
	bitmap = SetBitmapBit(bitmap, 9)
	if !HasBitmapBit(bitmap, 0) || !HasBitmapBit(bitmap, 9) {
		t.Fatalf("bitmap does not contain set bits: %08b", bitmap)
	}
	if HasBitmapBit(bitmap, 1) || HasBitmapBit(bitmap, -1) {
		t.Fatalf("bitmap contains unexpected bit")
	}
}