package normalizer

import (
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	in := `<div><strong>About us:</strong></div><p>We build &amp; ship.</p><ul><li>Go</li><li>Postgres&nbsp;SQL</li></ul>`
	got := HTMLToText(in)
	// Exact blank-line layout is not important; content and order are.
	for _, frag := range []string{"About us:", "We build & ship.", "- Go", "- Postgres SQL"} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in %q", frag, got)
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
