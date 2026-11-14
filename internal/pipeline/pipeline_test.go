package pipeline

import (
	"testing"
	"time"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/raw"
)

func TestTitleCase(t *testing.T) {
	cases := map[string]string{
		"paytm":       "Paytm",
		"mind-tickle": "Mind Tickle",
		"a_b.c":       "A B C",
		"x":           "X",
	}
	for in, want := range cases {
		if got := TitleCase(in); got != want {
			t.Errorf("TitleCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToInputCountryPrecedence(t *testing.T) {
	posted := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	base := fetcher.RawJob{
		ExternalID: "1", Title: "  Senior   Go Dev ", Location: "Pune, India",
		DescriptionHTML: "<p>Hello &amp; welcome</p>", Payload: []byte(`{"id":1}`), PostedAt: &posted,
	}

	// Text hint when the source states nothing.
	in, err := toInput(7, base)
	if err != nil {
		t.Fatal(err)
	}
	if in.Job.Country == nil || *in.Job.Country != "IN" {
		t.Errorf("expected IN from the location text, got %v", in.Job.Country)
	}
	if in.Job.TitleNormalized != "senior go dev" || in.Job.Description != "Hello & welcome" {
		t.Errorf("normalization wrong: %+v", in.Job)
	}
	if got, err := raw.Decompress(in.RawGzip); err != nil || string(got) != `{"id":1}` {
		t.Errorf("raw payload must round-trip, got %q, %v", got, err)
	}

	// The source's own country wins over the text hint.
	base.Country = "us"
	in, _ = toInput(7, base)
	if in.Job.Country == nil || *in.Job.Country != "US" {
		t.Errorf("source country should win and be upper-cased, got %v", in.Job.Country)
	}

	// Unknown stays unknown.
	base.Country, base.Location = "", "Remote - EMEA"
	in, _ = toInput(7, base)
	if in.Job.Country != nil {
		t.Errorf("unknown country should stay nil, got %v", *in.Job.Country)
	}
}
