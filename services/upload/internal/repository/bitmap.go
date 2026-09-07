package repository

func SetBitmapBit(bitmap []byte, index int) []byte {
	if index < 0 {
		return bitmap
	}
	byteIndex := index / 8
	if byteIndex >= len(bitmap) {
		expanded := make([]byte, byteIndex+1)
		copy(expanded, bitmap)
		bitmap = expanded
	} else {
		bitmap = append([]byte(nil), bitmap...)
	}
	bitmap[byteIndex] |= 1 << uint(index%8)
	return bitmap
}

func HasBitmapBit(bitmap []byte, index int) bool {
	if index < 0 || index/8 >= len(bitmap) {
		return false
	}
	return bitmap[index/8]&(1<<uint(index%8)) != 0
}
