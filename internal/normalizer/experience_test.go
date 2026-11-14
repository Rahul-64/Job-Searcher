package normalizer

import "testing"

func TestParseExperience(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // formatYears output: "5+", "3-5", "-" for nothing
	}{
		// phrases lifted from real job descriptions
		{"plus", "8+ years in industry experience and a degree (Master's)", "8+"},
		{"plus of", "5+ years of experience with BS/masters degree", "5+"},
		{"hands-on", "3+ years of hands-on Atlassian administration experience", "3+"},
		{"plain", "3 years of relevant experience in a customer service role", "3+"},
		{"range dash", "3-5 years of experience in backend development", "3-5"},
		{"range en dash", "2–4 years of professional experience", "2-4"},
		{"range to", "You have 4 to 6 years of experience building APIs", "4-6"},
		{"yrs", "Minimum 6 yrs experience in sales", "6+"},
		{"experience first", "Experience: 7+ years in product marketing", "7+"},

		// several requirements: the overall (largest) one wins
		{"overall vs skill", "8+ years of experience overall, including 2+ years of experience leading teams", "8+"},
		{"stack", "5+ years of experience in backend, 3+ years of experience with Kubernetes", "5+"},

		// fresher wording
		{"fresher", "Freshers are welcome to apply", "0+"},
		{"entry level", "This is an entry-level position", "0+"},
		{"zero to two", "Looking for 0-2 years of experience", "0-2"},

		// things that are not a candidate requirement
		{"company age", "Founded 15 years ago, we have grown into a global team", "-"},
		{"timespan", "Our goal is to double revenue within 3 years and grow the team", "-"},
		{"no context", "The role is based in our office for 2 years minimum lease", "-"},
		{"company history", "With over 20 years of experience we serve customers worldwide", "-"},
		{"implausible", "99 years of experience", "-"},
		{"bad range", "10-5 years of experience", "-"},
		{"empty", "", "-"},
		{"no numbers", "Strong communication skills and a passion for learning", "-"},
	}
	for _, c := range cases {
		min, max := ParseExperience(c.in)
		if got := formatYears(min, max); got != c.want {
			t.Errorf("%s: ParseExperience(%q) = %s, want %s", c.name, c.in, got, c.want)
		}
	}
}
