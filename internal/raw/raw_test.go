package raw

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := []byte(strings.Repeat(`{"title":"Backend Engineer","location":"Bengaluru"}`, 200))
	z, err := Compress(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(z) >= len(in) {
		t.Errorf("expected compression, got %d >= %d", len(z), len(in))
	}
	out, err := Decompress(z)
	if err != nil || !bytes.Equal(in, out) {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestDecompressRejectsGarbage(t *testing.T) {
	if _, err := Decompress([]byte("not gzip")); err == nil {
		t.Fatal("expected error")
	}
}
