package blockkey

import "testing"

func TestFor(t *testing.T) {
	valid := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got, err := For(valid)
	if err != nil {
		t.Fatalf("valid hash: %v", err)
	}
	want := "blocks/e3/b0/" + valid
	if got != want {
		t.Fatalf("For() = %q, want %q", got, want)
	}

	invalid := map[string]string{
		"":            "empty",
		"abc":         "too short",
		valid + "zz":  "too long",
		"g3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855": "not hex",
		"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855": "uppercase",
	}
	for hash, label := range invalid {
		if _, err := For(hash); err == nil {
			t.Errorf("%s: want error for %q", label, hash)
		}
	}
}
