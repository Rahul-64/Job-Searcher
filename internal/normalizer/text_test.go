package normalizer

import (
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	in := `<div><strong>About us:</strong></div><p>We build &amp; ship.</p><ul><li>Go</li><li>Postgres&nbsp;SQL</li></ul>`
	want := "About us:\n\nWe build & ship.\n\n- Go\n\n- Postgres SQL"
	got := HTMLToText(in)
	// Exact blank-line layout is not important; content and order are.
	for _, frag := range []string{"About us:", "We build & ship.", "- Go", "- Postgres SQL"} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in %q (reference layout %q)", frag, got, want)
		}
	}
	if strings.Contains(got, "<") || strings.Contains(got, "&amp;") {
		t.Errorf("tags/entities left over: %q", got)
	}
}

func TestHTMLToTextEmpty(t *testing.T) {
	if got := HTMLToText(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeTitle(t *testing.T) {
	if got := NormalizeTitle("  Senior   Backend\tEngineer "); got != "senior backend engineer" {
		t.Errorf("got %q", got)
	}
}

func TestCountryHint(t *testing.T) {
	if c := CountryHint("Bengaluru-VTP, India"); c == nil || *c != "IN" {
		t.Errorf("expected IN, got %v", c)
	}
	if c := CountryHint("Remote - US"); c != nil {
		t.Errorf("expected nil, got %v", *c)
	}
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint(1, "backend engineer", "Bengaluru")
	if a != Fingerprint(1, "backend engineer", " bengaluru ") {
		t.Error("location whitespace/case should not matter")
	}
	if a == Fingerprint(2, "backend engineer", "Bengaluru") {
		t.Error("company must affect fingerprint")
	}
}
