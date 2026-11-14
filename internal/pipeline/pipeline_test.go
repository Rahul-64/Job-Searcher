package pipeline

import (
	"testing"
	"time"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/raw"
	"jobscraper/internal/store"
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

func TestInScope(t *testing.T) {
	in := []store.Place{{City: "Pune", Country: "IN"}}
	us := []store.Place{{City: "San Francisco", Country: "US"}}
	mixed := []store.Place{{Country: "US"}, {City: "Bengaluru", Country: "IN"}}
	cases := []struct {
		name    string
		places  []store.Place
		targets []string
		want    bool
	}{
		{"india job, india target", in, []string{"IN"}, true},
		{"us job, india target", us, []string{"IN"}, false},
		{"job open in both", mixed, []string{"IN"}, true},
		{"unresolved location is kept", nil, []string{"IN"}, true},
		{"no targets keeps everything", us, nil, true},
		{"multiple targets", us, []string{"IN", "US"}, true},
	}
	for _, c := range cases {
		if got := InScope(c.places, c.targets); got != c.want {
			t.Errorf("%s: InScope = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestToInputDerivesFields(t *testing.T) {
	posted := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	j := fetcher.RawJob{
		ExternalID: "1", Title: "  Sr.  Go Dev ", Location: "Bangalore, IND; Pune, IN",
		DescriptionHTML: "<p>5+ years of experience required.</p><p>Base salary range is ₹2,400,000—₹3,600,000 per annum.</p>",
		Payload:         []byte(`{"id":1}`), PostedAt: &posted,
		Employment: "Full Time Employee", Remote: "hybrid",
	}
	in, err := toInput(7, j)
	if err != nil {
		t.Fatal(err)
	}
	f := in.Job
	if f.TitleNormalized != "senior go developer" || f.Seniority == nil || *f.Seniority != "senior" {
		t.Errorf("title/seniority: %q / %v", f.TitleNormalized, f.Seniority)
	}
	if f.Country == nil || *f.Country != "IN" || f.City == nil || *f.City != "Bengaluru" || f.Region == nil || *f.Region != "Karnataka" {
		t.Errorf("primary place: %v %v %v", f.City, f.Region, f.Country)
	}
	if len(f.Places) != 2 {
		t.Errorf("places: %+v", f.Places)
	}
	if f.RemoteType == nil || *f.RemoteType != "hybrid" || f.EmploymentType == nil || *f.EmploymentType != "full_time" {
		t.Errorf("remote/employment: %v / %v", f.RemoteType, f.EmploymentType)
	}
	if f.ExpMin == nil || *f.ExpMin != 5 {
		t.Errorf("experience: %v", f.ExpMin)
	}
	if f.SalaryMin == nil || *f.SalaryMin != 2400000 || f.SalaryCurrency == nil || *f.SalaryCurrency != "INR" {
		t.Errorf("salary: %v %v", f.SalaryMin, f.SalaryCurrency)
	}
	if f.NormVersion < 1 || f.Fingerprint == "" {
		t.Errorf("version/fingerprint: %d %q", f.NormVersion, f.Fingerprint)
	}
	if got, err := raw.Decompress(in.RawGzip); err != nil || string(got) != `{"id":1}` {
		t.Errorf("raw payload must round-trip, got %q, %v", got, err)
	}

	// A bare job: unknown fields stay nil rather than being guessed.
	in, _ = toInput(7, fetcher.RawJob{ExternalID: "2", Title: "Engineer", Payload: []byte(`{}`)})
	f = in.Job
	if f.Country != nil || f.RemoteType != nil || f.Seniority != nil || f.SalaryMin != nil || f.ExpMin != nil || len(f.Places) != 0 {
		t.Errorf("unknown fields must stay nil: %+v", f)
	}
}

func TestToInputUsesSourceCountryAndStructuredSalary(t *testing.T) {
	in, err := toInput(1, fetcher.RawJob{
		ExternalID: "1", Title: "Designer", Location: "EMEA", Country: "DE",
		SalaryMin: 50000, SalaryMax: 70000, SalaryCurrency: "EUR", SalaryPeriod: "year",
		DescriptionHTML: "Salary $1,000,000 - $2,000,000 per year", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Job.Country == nil || *in.Job.Country != "DE" {
		t.Errorf("source country should fill an unresolvable location: %v", in.Job.Country)
	}
	if in.Job.SalaryCurrency == nil || *in.Job.SalaryCurrency != "EUR" {
		t.Errorf("structured salary should win over text: %v", in.Job.SalaryCurrency)
	}
}
