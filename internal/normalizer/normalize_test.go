package normalizer

import "testing"

func TestNormalizeTitle(t *testing.T) {
	cases := map[string]string{
		"  Senior   Backend\tEngineer ":  "senior backend engineer",
		"Sr. Software Engg":              "senior software engineer",
		"Sr SWE - Payments":              "senior software engineer payments",
		"SDE II":                         "software development engineer ii",
		"Jr. Dev & Ops":                  "junior developer and ops",
		"Engineering Mgr, Platform":      "engineering manager platform",
		"Node.js Developer (C++/Rust)":   "node.js developer c++ rust",
		"VP, Engineering":                "vice president engineering",
		"Customer Support Specialist II": "customer support specialist ii",
		"":                               "",
	}
	for in, want := range cases {
		if got := NormalizeTitle(in); got != want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSeniority(t *testing.T) {
	cases := map[string]string{
		"Software Engineering Intern":     Intern,
		"Summer Internship, Data":         Intern,
		"Staff Software Engineer":         Staff,
		"Principal Engineer":              Principal,
		"Senior Staff Engineer":           Staff, // the higher signal wins
		"Sr. Software Engineer":           Senior,
		"Senior Director, Engineering":    Director,
		"Associate Director of Sales":     Director,
		"Head of Product":                 Director,
		"Vice President, Sales":           Executive,
		"Chief Technology Officer":        Executive,
		"Team Lead, Payments":             Lead,
		"Tech Lead":                       Lead,
		"Software Engineer II":            Mid,
		"Customer Support Specialist III": Senior,
		"Junior Designer":                 Junior,
		"Associate Product Manager":       Junior,
		"Software Engineer I":             Junior,
		"Graduate Software Engineer":      Junior,
		"Software Engineer":               "", // no signal -> no guess
		"Product Manager":                 "",
		"Lead Generation Specialist":      "", // "lead" is not a level here
		"Staff Accountant":                "", // "staff" is not a level here
		"Senior Lead Engineer":            Lead,
	}
	for in, want := range cases {
		if got := Seniority(NormalizeTitle(in)); got != want {
			t.Errorf("Seniority(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmployment(t *testing.T) {
	cases := map[string]string{
		"Full Time Employee": FullTime, "FullTime": FullTime, "Part Time": PartTime, "PartTime": PartTime,
		"Intern": InternEmp, "Contractor": Contract, "Contract": Contract, "Temporary": Temporary, "": "", "Other": "",
	}
	for in, want := range cases {
		if got := NormalizeEmployment(in); got != want {
			t.Errorf("NormalizeEmployment(%q) = %q, want %q", in, got, want)
		}
	}
	if got := EmploymentFromTitle(NormalizeTitle("Marketing Intern")); got != InternEmp {
		t.Errorf("title intern: %q", got)
	}
	if got := EmploymentFromTitle(NormalizeTitle("Contract Designer (6 months)")); got != Contract {
		t.Errorf("title contract: %q", got)
	}
	if got := EmploymentFromTitle(NormalizeTitle("Software Engineer")); got != "" {
		t.Errorf("no signal should be empty, got %q", got)
	}
}

func TestDetectRemote(t *testing.T) {
	loc := func(s string) LocationResult { return ParseLocation(s) }
	cases := []struct {
		name, hint, location, title, desc, want string
	}{
		{"source says remote", "Remote", "Bengaluru", "", "", Remote},
		{"source says onsite", "OnSite", "Remote - US", "", "", Onsite}, // source wins over location text
		{"source underscore", "on_site", "", "", "", Onsite},
		{"source unspecified falls through", "unspecified", "Remote - India", "", "", Remote},
		{"location remote", "", "Remote - USA", "", "", Remote},
		{"location hybrid", "", "Pune, India (Hybrid)", "", "", Hybrid},
		{"title remote", "", "United States", "Senior Engineer (Remote)", "", Remote},
		{"description fully remote", "", "United States", "Engineer", "This is a fully remote role open across the US.", Remote},
		{"description remote-first", "", "", "Engineer", "We are a remote-first company.", Remote},
		{"description hybrid", "", "Bengaluru", "Engineer", "We follow a hybrid work model with 3 days in office.", Hybrid},
		{"description onsite", "", "Bengaluru", "Engineer", "This is an on-site role at our Bengaluru campus.", Onsite},
		{"no signal stays empty", "", "Bengaluru", "Engineer", "Join our team to build great products.", ""},
	}
	for _, c := range cases {
		if got := DetectRemote(c.hint, loc(c.location), c.title, c.desc); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizeEndToEnd(t *testing.T) {
	out := Normalize(Input{
		CompanyID:     7,
		Title:         "  Sr. Software Engineer II  ",
		Location:      "Bangalore, IND; Pune, IN",
		Description:   "We need 5+ years of experience in Go. The base salary range is $168,000—$203,000 per year. This is a hybrid work model.",
		SourceCountry: "IN", SourceEmployment: "Full Time Employee",
	})
	if out.TitleNormalized != "senior software engineer ii" {
		t.Errorf("title: %q", out.TitleNormalized)
	}
	if out.Seniority != Senior { // "senior" outranks the "II" marker
		t.Errorf("seniority: %q", out.Seniority)
	}
	if len(out.Places) != 2 || out.Primary() != (Place{"Bengaluru", "Karnataka", "IN"}) {
		t.Errorf("places: %+v", out.Places)
	}
	if out.EmploymentType != FullTime || out.RemoteType != Hybrid {
		t.Errorf("employment/remote: %q / %q", out.EmploymentType, out.RemoteType)
	}
	if out.ExpMin == nil || *out.ExpMin != 5 || out.ExpMax != nil {
		t.Errorf("experience: %s", formatYears(out.ExpMin, out.ExpMax))
	}
	if out.Salary == nil || out.Salary.Min != 168000 || out.Salary.Currency != "USD" || out.Salary.Period != "year" {
		t.Errorf("salary: %+v", out.Salary)
	}
	if out.Version != Version || out.Fingerprint == "" {
		t.Errorf("version/fingerprint: %d %q", out.Version, out.Fingerprint)
	}
}

func TestNormalizeSourceHintsAndFallbacks(t *testing.T) {
	// Structured salary beats text; source country fills an unresolvable location.
	src := &Salary{Min: 100, Max: 200, Currency: "EUR", Period: "year"}
	out := Normalize(Input{
		Title: "Engineer", Location: "EMEA", Description: "Salary $1,000,000 - $2,000,000 per year",
		SourceCountry: "de", SourceSalary: src,
	})
	if out.Salary == nil || out.Salary.Currency != "EUR" {
		t.Errorf("structured salary should win: %+v", out.Salary)
	}
	if out.Primary() != (Place{Country: "DE"}) {
		t.Errorf("source country fallback: %+v", out.Places)
	}

	// A zero-valued structured range (Lever sends min=max=0) must not mask text.
	out = Normalize(Input{Title: "Engineer", Description: "Base salary $90,000 - $120,000 per year",
		SourceSalary: &Salary{Currency: "INR", Period: "year"}})
	if out.Salary == nil || out.Salary.Currency != "USD" {
		t.Errorf("empty structured salary must fall back to text: %+v", out.Salary)
	}

	// Nothing to find: all optional fields stay empty.
	out = Normalize(Input{Title: "Engineer", Location: "", Description: "We build things."})
	if out.Seniority != "" || out.RemoteType != "" || out.Salary != nil || out.ExpMin != nil || len(out.Places) != 0 {
		t.Errorf("empty input should yield empty fields: %+v", out)
	}
}

func TestFingerprintMergesOnlyTrueDuplicates(t *testing.T) {
	p := Place{"Bengaluru", "Karnataka", "IN"}
	a := Fingerprint(1, "staff software engineer", p, "Join the payments team to build ledgers.")
	if a != Fingerprint(1, "staff software engineer", p, "  JOIN the payments   team to build ledgers. ") {
		t.Error("whitespace and case differences must not change the fingerprint")
	}
	if a == Fingerprint(1, "staff software engineer", p, "Join the search team to build ranking.") {
		t.Error("same title and city but a different description is a different job")
	}
	if a == Fingerprint(2, "staff software engineer", p, "Join the payments team to build ledgers.") {
		t.Error("company must affect the fingerprint")
	}
	if a == Fingerprint(1, "staff software engineer", Place{"Pune", "Maharashtra", "IN"}, "Join the payments team to build ledgers.") {
		t.Error("place must affect the fingerprint")
	}
}
