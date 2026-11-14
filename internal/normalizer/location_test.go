package normalizer

import (
	"reflect"
	"strings"
	"testing"
)

func P(city, region, country string) Place {
	return Place{City: city, Region: region, Country: country}
}

// Cases are taken from real location strings in the scraped data.
func TestParseLocation(t *testing.T) {
	cases := []struct {
		in     string
		want   []Place
		remote bool
		hybrid bool
	}{
		// plain cities
		{"San Francisco", []Place{P("San Francisco", "California", "US")}, false, false},
		{"Bengaluru, India", []Place{P("Bengaluru", "Karnataka", "IN")}, false, false},
		{"Bangalore, Karnataka", []Place{P("Bengaluru", "Karnataka", "IN")}, false, false},
		{"Bangalore, IND", []Place{P("Bengaluru", "Karnataka", "IN")}, false, false},
		{"Bengaluru-VTP, India", []Place{P("Bengaluru", "Karnataka", "IN")}, false, false},
		{"Gurugram", []Place{P("Gurugram", "Haryana", "IN")}, false, false},
		{"Gurgaon, IND", []Place{P("Gurugram", "Haryana", "IN")}, false, false},
		{"Noida, Uttar Pradesh", []Place{P("Noida", "Uttar Pradesh", "IN")}, false, false},
		{"Pune, IN", []Place{P("Pune", "Maharashtra", "IN")}, false, false},
		{"Delhi NCR", []Place{P("Delhi", "Delhi", "IN")}, false, false},
		{"Delhi/NCR", []Place{P("Delhi", "Delhi", "IN")}, false, false},

		// countries and aliases
		{"Tokyo, Japan", []Place{P("Tokyo", "", "JP")}, false, false},
		{"Tokyo, JPN", []Place{P("Tokyo", "", "JP")}, false, false},
		{"London, UK", []Place{P("London", "England", "GB")}, false, false},
		{"London, United Kingdom", []Place{P("London", "England", "GB")}, false, false},
		{"Dublin", []Place{P("Dublin", "", "IE")}, false, false},
		{"Singapore", []Place{P("Singapore", "", "SG")}, false, false},
		{"Singapore, Singapore", []Place{P("Singapore", "", "SG")}, false, false},
		{"Seoul, South Korea", []Place{P("Seoul", "", "KR")}, false, false},
		{"Mexico City", []Place{P("Mexico City", "", "MX")}, false, false},
		{"United States", []Place{P("", "", "US")}, false, false},
		{"Spain", []Place{P("", "", "ES")}, false, false},

		// US states, codes and the IN / CA / DC ambiguities
		{"New York, New York, USA", []Place{P("New York", "New York", "US")}, false, false},
		{"New York City", []Place{P("New York", "New York", "US")}, false, false},
		{"Santa Clara, California, USA", []Place{P("Santa Clara", "California", "US")}, false, false},
		{"Palo Alto, CA", []Place{P("Palo Alto", "California", "US")}, false, false},
		{"Washington, DC", []Place{P("Washington", "District of Columbia", "US")}, false, false},
		{"Seattle, Washington", []Place{P("Seattle", "Washington", "US")}, false, false},
		{"Indianapolis, IN", []Place{P("Indianapolis", "Indiana", "US")}, false, false},
		{"Toronto, Ontario, Canada", []Place{P("Toronto", "Ontario", "CA")}, false, false},
		{"Austin, TX", []Place{P("Austin", "Texas", "US")}, false, false},
		{"Dublin, Ohio", []Place{P("Dublin", "Ohio", "US")}, false, false}, // a full region name overrules the default
		{"Ashburn, VA", []Place{P("Ashburn", "Virginia", "US")}, false, false},
		{"Foo, IN", []Place{P("Foo", "", "IN")}, false, false}, // unknown city, IN = India, never Indiana

		// several places
		{"San Francisco / New York City", []Place{P("San Francisco", "California", "US"), P("New York", "New York", "US")}, false, false},
		{"Mumbai/Delhi/Bengaluru", []Place{P("Mumbai", "Maharashtra", "IN"), P("Delhi", "Delhi", "IN"), P("Bengaluru", "Karnataka", "IN")}, false, false},
		{"Mumbai, Delhi", []Place{P("Mumbai", "Maharashtra", "IN"), P("Delhi", "Delhi", "IN")}, false, false}, // two cities, not city+state
		{"Bengaluru; Gurugram; Mumbai", []Place{P("Bengaluru", "Karnataka", "IN"), P("Gurugram", "Haryana", "IN"), P("Mumbai", "Maharashtra", "IN")}, false, false},
		{"Bangalore, IND; Bangalore, IND; Mohali, IND", []Place{P("Bengaluru", "Karnataka", "IN"), P("Mohali", "Punjab", "IN")}, false, false},
		{"Mountain View, California; San Francisco, California", []Place{P("Mountain View", "California", "US"), P("San Francisco", "California", "US")}, false, false},

		// remote / hybrid markers
		{"Remote - USA", []Place{P("", "", "US")}, true, false},
		{"Remote, United States", []Place{P("", "", "US")}, true, false},
		{"Remote - India", []Place{P("", "", "IN")}, true, false},
		{"Remote (US)", []Place{P("", "", "US")}, true, false},
		{"Remote, Canada; Remote, United States", []Place{P("", "", "CA"), P("", "", "US")}, true, false},
		{"Bangalore, IN ; Pune, IN; Remote", []Place{P("Bengaluru", "Karnataka", "IN"), P("Pune", "Maharashtra", "IN")}, true, false},
		{"Hybrid - Bengaluru", []Place{P("Bengaluru", "Karnataka", "IN")}, false, true},
		{"Pune, India (Hybrid)", []Place{P("Pune", "Maharashtra", "IN")}, false, true},
		{"Remote", nil, true, false},
		{"Remote/Hybrid", nil, true, true},

		// alternatives and descriptors (found in real data)
		{"Sydney Or Melbourne", []Place{P("Sydney", "", "AU"), P("Melbourne", "", "AU")}, false, false},
		{"London OR Dublin", []Place{P("London", "England", "GB"), P("Dublin", "", "IE")}, false, false},
		{"Poland OR Romania", []Place{P("", "", "PL"), P("", "", "RO")}, false, false},
		{"Europe or US", []Place{P("", "", "US")}, false, false},
		{"East Coast US", []Place{P("", "", "US")}, false, false},
		{"Central US", []Place{P("", "", "US")}, false, false},
		{"Remote - Washington D.C.", []Place{P("Washington", "District of Columbia", "US")}, true, false},
		{"NAMER and LatAm", nil, false, false},
		{"USCA", nil, false, false},
		{"LOCATION", nil, false, false},
		{"na", nil, false, false},

		// no usable place
		{"", nil, false, false},
		{"EMEA", nil, false, false},
		{"Tallaght", nil, false, false}, // unknown city and no country: not stored
		{"N/A", nil, false, false},
	}
	for _, c := range cases {
		got := ParseLocation(c.in)
		if !reflect.DeepEqual(got.Places, c.want) {
			t.Errorf("ParseLocation(%q)\n   got  %+v\n   want %+v", c.in, got.Places, c.want)
		}
		if got.Remote != c.remote || got.Hybrid != c.hybrid {
			t.Errorf("ParseLocation(%q) remote=%v hybrid=%v, want remote=%v hybrid=%v", c.in, got.Remote, got.Hybrid, c.remote, c.hybrid)
		}
	}
}

func TestCountryCode(t *testing.T) {
	cases := map[string]string{
		"India": "IN", "india": "IN", "IND": "IN", "IN": "IN", "United States": "US",
		"USA": "US", "uk": "GB", "Japan": "JP", "JPN": "JP", "Türkiye": "TR",
	}
	for in, want := range cases {
		if got, ok := CountryCode(in); !ok || got != want {
			t.Errorf("CountryCode(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "Atlantis", "in", "xx"} {
		if got, ok := CountryCode(bad); ok {
			t.Errorf("CountryCode(%q) = %q, want no match", bad, got)
		}
	}
}

// The reference data must be internally consistent, or lookups silently go wrong.
func TestReferenceDataIntegrity(t *testing.T) {
	if len(data.cities) < 150 {
		t.Fatalf("expected a few hundred cities, got %d", len(data.cities))
	}
	seen := map[string]bool{}
	for _, c := range data.cities {
		key := strings.ToLower(c.Name) + "|" + c.Country
		if seen[key] {
			t.Errorf("duplicate city %s", key)
		}
		seen[key] = true

		if c.Country == "US" || c.Country == "CA" || c.Country == "IN" {
			rg, ok := data.regionByName[strings.ToLower(c.Region)]
			if !ok {
				t.Errorf("city %s: region %q is not in regions.csv", c.Name, c.Region)
			} else if rg.Country != c.Country {
				t.Errorf("city %s: region %q belongs to %s, city is in %s", c.Name, c.Region, rg.Country, c.Country)
			}
		}
		for _, a := range c.Aliases {
			if a != strings.ToLower(a) || strings.Contains(a, ",") {
				t.Errorf("city %s: alias %q must be lower case with no comma", c.Name, a)
			}
		}
	}
	// every Indian city resolves through its own name and every alias
	for _, c := range data.cities {
		for _, key := range append([]string{strings.ToLower(c.Name)}, c.Aliases...) {
			if len(data.cityByAlias[key]) == 0 {
				t.Errorf("alias %q of %s is not indexed", key, c.Name)
			}
		}
	}
}
