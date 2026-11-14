package normalizer

import "testing"

func TestParseSalary(t *testing.T) {
	type want struct {
		min, max float64
		cur, per string
	}
	cases := []struct {
		name string
		in   string
		want *want
	}{
		// formats seen in the scraped data
		{"usd em dash", "The base salary range for this role is $204,000—$255,000 USD per year.", &want{204000, 255000, "USD", "year"}},
		{"usd en dash", "Pay range: $168,000 – $203,000", &want{168000, 203000, "USD", "year"}},
		{"eur european thousands", "Gross annual salary €48.000—€56.000 depending on experience", &want{48000, 56000, "EUR", "year"}},
		{"inr absolute", "The compensation range is ₹5,900,000—₹8,500,000 per annum", &want{5900000, 8500000, "INR", "year"}},
		{"gbp", "Salary: £55,000 - £70,000 per year", &want{55000, 70000, "GBP", "year"}},

		// Indian formats
		{"lpa", "CTC range 12-18 LPA for this position", &want{1200000, 1800000, "INR", "year"}},
		{"lakh suffix", "Salary ₹12L - ₹18L", &want{1200000, 1800000, "INR", "year"}},
		{"inr monthly", "Salary INR 40,000 - 60,000 per month", &want{40000, 60000, "INR", "month"}},

		// other shapes
		{"k suffix", "Compensation: $120k-$150k annually", &want{120000, 150000, "USD", "year"}},
		{"hourly", "The hourly rate is $40 - $60 per hour", &want{40, 60, "USD", "hour"}},
		{"cad code", "Annual salary CAD $90,000 - $120,000", &want{90000, 120000, "CAD", "year"}},
		{"first valid range wins", "Base pay $100,000 - $130,000 (Zone A). Base pay $120,000 - $150,000 (Zone B).", &want{100000, 130000, "USD", "year"}},

		// must NOT be read as salary
		{"signing bonus after", "We offer a salary plus $5,000 - $10,000 signing bonus", nil},
		{"bonus before", "Annual bonus of $5,000 - $10,000 for top performers", nil},
		{"no pay vocabulary", "We shipped $100,000 - $200,000 worth of software", nil},
		{"no currency", "Join our team of 10-15 engineers; compensation is competitive", nil},
		{"year range", "Salary will be reviewed 2020-2024", nil},
		{"equal values no period", "Pay: $40—$40", nil},
		{"tiny amount no period", "Salary range $100 - $200", nil},
		{"absurd ratio", "Salary $1,000 - $900,000", nil},
		{"descending", "Salary $200,000 - $100,000", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		got := ParseSalary(c.in)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: expected no salary, got %+v", c.name, *got)
		case c.want != nil && got == nil:
			t.Errorf("%s: expected %+v, got nothing", c.name, *c.want)
		case c.want != nil && (got.Min != c.want.min || got.Max != c.want.max || got.Currency != c.want.cur || got.Period != c.want.per):
			t.Errorf("%s: got %+v, want %+v", c.name, *got, *c.want)
		}
	}
}

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in, suffix string
		want       float64
	}{
		{"88,000", "", 88000},
		{"5,900,000", "", 5900000},
		{"48.000", "", 48000}, // European thousands
		{"120.5", "k", 120500},
		{"12", "LPA", 1200000},
		{"1.5", "cr", 15000000},
		{"7,5", "", 7.5},
	}
	for _, c := range cases {
		got, ok := parseAmount(c.in, c.suffix)
		if !ok || got != c.want {
			t.Errorf("parseAmount(%q,%q) = %v, %v; want %v", c.in, c.suffix, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3,4,5"} {
		if _, ok := parseAmount(bad, ""); ok {
			t.Errorf("parseAmount(%q) should fail", bad)
		}
	}
}
