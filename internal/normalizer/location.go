package normalizer

import (
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

//go:embed data/*.csv
var dataFS embed.FS

// Place is one normalized location. Country is always set; City and Region may
// be empty for country- or region-level places ("United States", "Remote, Canada").
type Place struct {
	City    string
	Region  string
	Country string // ISO 3166-1 alpha-2
}

// City is a reference-data city (see data/*.csv).
type City struct {
	Name    string
	Region  string
	Country string
	Aliases []string // lower case, includes no copy of Name
}

type region struct {
	Country string
	Code    string // empty for regions without a usable code
	Name    string
}

type dataset struct {
	countryByName map[string]string // lower-case name / alias / iso3 -> ISO2
	iso2          map[string]bool   // "IN" -> true
	iso3          map[string]string // "IND" -> "IN"
	regionByName  map[string]region // lower-case name
	regionByCode  map[string]region // "CA" -> California
	cities        []City
	cityByAlias   map[string][]City // lower-case name or alias -> candidates, primary first
}

var data = mustLoadData()

// Cities returns all reference cities (for seeding the locations tables).
func Cities() []City { return data.cities }

// CountryCode resolves a country name or code ("India", "IND", "united states") to ISO 3166-1 alpha-2.
func CountryCode(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if len(s) == 2 && data.iso2[strings.ToUpper(s)] && s == strings.ToUpper(s) {
		return s, true
	}
	if c, ok := data.iso3[strings.ToUpper(s)]; ok && len(s) == 3 {
		return c, true
	}
	c, ok := data.countryByName[strings.ToLower(s)]
	return c, ok
}

func readCSV(name string) ([][]string, error) {
	f, err := dataFS.Open("data/" + name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comment = '#'
	r.FieldsPerRecord = -1
	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		rows = append(rows, rec)
	}
}

func splitAliases(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ";") {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func mustLoadData() *dataset {
	d, err := loadData()
	if err != nil {
		panic("normalizer: bad embedded reference data: " + err.Error())
	}
	return d
}

func loadData() (*dataset, error) {
	d := &dataset{
		countryByName: map[string]string{},
		iso2:          map[string]bool{},
		iso3:          map[string]string{},
		regionByName:  map[string]region{},
		regionByCode:  map[string]region{},
		cityByAlias:   map[string][]City{},
	}

	rows, err := readCSV("countries.csv")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) < 3 {
			return nil, fmt.Errorf("countries.csv: short row %v", r)
		}
		c2, c3, name := strings.ToUpper(r[0]), strings.ToUpper(r[1]), strings.TrimSpace(r[2])
		d.iso2[c2] = true
		d.iso3[c3] = c2
		if name != "" {
			d.countryByName[strings.ToLower(name)] = c2
		}
		if len(r) > 3 {
			for _, a := range splitAliases(r[3]) {
				d.countryByName[a] = c2
			}
		}
	}

	if rows, err = readCSV("regions.csv"); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) != 3 {
			return nil, fmt.Errorf("regions.csv: bad row %v", r)
		}
		rg := region{Country: strings.ToUpper(r[0]), Code: strings.ToUpper(r[1]), Name: r[2]}
		d.regionByName[strings.ToLower(rg.Name)] = rg
		if rg.Code != "" {
			d.regionByCode[rg.Code] = rg
		}
	}

	for _, file := range []string{"india_cities.csv", "world_cities.csv"} {
		if rows, err = readCSV(file); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if len(r) < 3 {
				return nil, fmt.Errorf("%s: short row %v", file, r)
			}
			c := City{Name: strings.TrimSpace(r[0]), Region: strings.TrimSpace(r[1]), Country: strings.ToUpper(strings.TrimSpace(r[2]))}
			if len(r) > 3 {
				c.Aliases = splitAliases(r[3])
			}
			if !d.iso2[c.Country] {
				return nil, fmt.Errorf("%s: city %q has unknown country %q", file, c.Name, c.Country)
			}
			d.cities = append(d.cities, c)
			for _, key := range append([]string{strings.ToLower(c.Name)}, c.Aliases...) {
				// First entry wins: the file order decides which meaning is primary.
				d.cityByAlias[key] = append(d.cityByAlias[key], c)
			}
		}
	}
	return d, nil
}

// ---- parsing ---------------------------------------------------------------

// LocationResult is the outcome of parsing a free-text location.
type LocationResult struct {
	Places []Place // primary first; may be empty if nothing could be resolved
	Remote bool    // the text says remote / work from home / anywhere
	Hybrid bool
}

var (
	macroRegions = map[string]bool{
		"emea": true, "apac": true, "latam": true, "americas": true, "europe": true, "asia": true,
		"africa": true, "north america": true, "south america": true, "asia pacific": true,
		"middle east": true, "global": true, "worldwide": true, "international": true, "nordics": true,
		"dach": true, "benelux": true, "mena": true, "oceania": true, "anz": true, "eu": true,
		"namer": true, "latam and namer": true, "usca": true, "na": true, "tbd": true, "location": true,
	}
	remoteRe  = regexp.MustCompile(`(?i)\b(remote(?:ly)?|work from home|wfh|anywhere|virtual|home[- ]based|telecommute|distributed)\b`)
	hybridRe  = regexp.MustCompile(`(?i)\bhybrid\b`)
	markerRe  = regexp.MustCompile(`(?i)\b(remote(?:ly)?|hybrid|on-?site|in[- ]office|work from home|wfh|anywhere|virtual|home[- ]based|telecommute|distributed)\b`)
	dashSepRe = regexp.MustCompile(`\s+[-–—]+\s+`)
	// "or" separates alternatives: "Sydney Or Melbourne", "London OR Dublin".
	segSepRe = regexp.MustCompile(`(?i)\s*[;|/\n]\s*|\s+or\s+`)
	parenRe  = regexp.MustCompile(`[()\[\]]`)
	spaceRe  = regexp.MustCompile(`\s+`)
	// "Bangkok based", "Pune Based", "Bangalore-based", "based in Pune": the word
	// only says the person works from there, so it is dropped before matching.
	basedRe = regexp.MustCompile(`(?i)\bbased(?:\s+in)?\b`)
)

// ParseLocation turns text such as "Bangalore, IND; Pune, IN", "Remote - USA" or
// "Mumbai/Delhi/Bengaluru" into places. It never fails: unresolvable text yields
// no places, and the caller keeps the raw string.
func ParseLocation(raw string) LocationResult {
	var res LocationResult
	s := spaceRe.ReplaceAllString(strings.TrimSpace(raw), " ")
	if s == "" {
		return res
	}
	res.Remote = remoteRe.MatchString(s)
	res.Hybrid = hybridRe.MatchString(s)

	s = dashSepRe.ReplaceAllString(s, ", ")
	s = parenRe.ReplaceAllString(s, ", ")

	seen := map[Place]bool{}
	for _, seg := range segSepRe.Split(s, -1) {
		for _, p := range resolveSegment(strings.Split(seg, ",")) {
			if !seen[p] {
				seen[p] = true
				res.Places = append(res.Places, p)
			}
		}
	}
	res.Places = dropVague(res.Places)
	return res
}

// dropVague removes country- or region-level places when a more specific place
// in the same country is present ("Pune, India; India" keeps only Pune).
func dropVague(in []Place) []Place {
	specific := map[string]bool{}
	for _, p := range in {
		if p.City != "" {
			specific[p.Country] = true
		}
	}
	out := in[:0:0]
	for _, p := range in {
		if p.City == "" && specific[p.Country] {
			continue
		}
		out = append(out, p)
	}
	return out
}

type hint struct {
	country string
	strong  bool // from a full name or ISO3; two-letter codes are weak (ambiguous)
}

type draft struct {
	name   string // city text as written (empty for country/region-only drafts)
	cands  []City // reference candidates for name, primary first
	region string // explicit region, canonical name
	hints  []hint
}

func (d *draft) addHint(c string, strong bool) {
	for i, h := range d.hints {
		if h.country == c {
			d.hints[i].strong = h.strong || strong
			return
		}
	}
	d.hints = append(d.hints, hint{c, strong})
}

func cleanToken(t string) string {
	t = markerRe.ReplaceAllString(t, " ")
	t = basedRe.ReplaceAllString(t, " ")
	t = spaceRe.ReplaceAllString(t, " ")
	return strings.Trim(t, " .,:;-–—")
}

type interp struct {
	region  *region
	country string
	strong  bool
}

// interpret lists every way a token can be read as a region or country.
func interpret(t, low string) []interp {
	var out []interp
	upper := t == strings.ToUpper(t)
	if rg, ok := data.regionByName[low]; ok {
		out = append(out, interp{region: &rg, country: rg.Country, strong: true})
	}
	if len(t) == 2 && upper {
		if rg, ok := data.regionByCode[t]; ok {
			out = append(out, interp{region: &rg, country: rg.Country})
		}
		if data.iso2[t] {
			out = append(out, interp{country: t})
		}
	}
	if len(t) == 3 && upper {
		if c, ok := data.iso3[t]; ok {
			out = append(out, interp{country: c, strong: true})
		}
	}
	// Full names and aliases ("united states", "uk", "us"). ISO2 codes are not in
	// this map, so a bare "in" or "id" can never match here.
	if c, ok := data.countryByName[low]; ok {
		out = append(out, interp{country: c, strong: true})
	}
	return out
}

// trailingCountry resolves a multi-word token whose last word is a country name.
func trailingCountry(low string) (string, bool) {
	words := strings.Fields(low)
	if len(words) < 2 {
		return "", false
	}
	c, ok := data.countryByName[words[len(words)-1]]
	return c, ok
}

func cityCandidates(low string) []City {
	if c := data.cityByAlias[low]; len(c) > 0 {
		return c
	}
	// "Bengaluru-VTP", "Noida-Sector 62": try the part before the first hyphen.
	if i := strings.IndexAny(low, "-–"); i >= 3 {
		return data.cityByAlias[strings.TrimSpace(low[:i])]
	}
	return nil
}

func resolveSegment(tokens []string) []Place {
	var drafts []*draft
	var cur *draft

	for _, raw := range tokens {
		t := cleanToken(raw)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if macroRegions[low] {
			continue
		}

		// A token after a place usually refines it: region, state code, country.
		if cur != nil {
			if in := interpret(t, low); len(in) > 0 && !startsNewCity(cur, in, low) {
				for _, it := range in {
					if it.region != nil && cur.region == "" {
						cur.region = it.region.Name
					}
					cur.addHint(it.country, it.strong)
				}
				continue
			}
		}
		if cands := cityCandidates(low); len(cands) > 0 {
			cur = &draft{name: t, cands: cands}
			drafts = append(drafts, cur)
			continue
		}
		if in := interpret(t, low); len(in) > 0 { // country- or region-only place
			cur = &draft{}
			for _, it := range in {
				if it.region != nil && cur.region == "" {
					cur.region = it.region.Name
				}
				cur.addHint(it.country, it.strong)
			}
			drafts = append(drafts, cur)
			continue
		}
		if cur == nil {
			// "East Coast US", "Central US": a descriptor we cannot place, but the
			// closing word names the country.
			if c, ok := trailingCountry(low); ok {
				cur = &draft{}
				cur.addHint(c, true)
				drafts = append(drafts, cur)
				continue
			}
			// Otherwise: probably a city we have no data for.
			cur = &draft{name: t}
			drafts = append(drafts, cur)
		}
		// Unknown trailing tokens (districts, office names) are ignored.
	}

	var out []Place
	for _, d := range drafts {
		if p, ok := d.place(); ok {
			out = append(out, p)
		}
	}
	return out
}

// startsNewCity reports whether a token that reads as a region is really the next
// city in a list: in "Mumbai, Delhi" Delhi is a city, but in "Seattle, Washington"
// or "Washington, DC" the token merely restates the place's own region.
func startsNewCity(cur *draft, in []interp, low string) bool {
	if len(cur.cands) == 0 || len(data.cityByAlias[low]) == 0 {
		return false
	}
	for _, it := range in {
		if it.region != nil && it.region.Name == cur.cands[0].Region {
			return false
		}
	}
	return true
}

func hintMatches(hints []hint, country string) bool {
	for _, h := range hints {
		if h.country == country {
			return true
		}
	}
	return false
}

func (d *draft) place() (Place, bool) {
	if len(d.cands) > 0 {
		for _, c := range d.cands {
			if hintMatches(d.hints, c.Country) {
				return Place{City: c.Name, Region: c.Region, Country: c.Country}, true
			}
		}
		// Only a full country/region name may overrule the reference data
		// ("Dublin, Ohio"); a bare two-letter code is too ambiguous ("Indianapolis, IN").
		for _, h := range d.hints {
			if h.strong {
				return Place{City: titleCase(d.name), Region: d.region, Country: h.country}, true
			}
		}
		c := d.cands[0]
		return Place{City: c.Name, Region: c.Region, Country: c.Country}, true
	}

	country := bestCountry(d.hints)
	if country == "" {
		return Place{}, false // a city name with no way to place it is not stored
	}
	// A region that belongs to a different country than the one we settled on
	// ("Foo, IN" read as India, but IN also matched Indiana) is dropped.
	region := d.region
	if rg, ok := data.regionByName[strings.ToLower(region)]; ok && rg.Country != country {
		region = ""
	}
	return Place{City: titleCase(d.name), Region: region, Country: country}, true
}

// bestCountry prefers a strong hint; among weak ones India wins, because
// "IN" is far more likely to mean India than Indiana in this corpus.
func bestCountry(hints []hint) string {
	for _, h := range hints {
		if h.strong {
			return h.country
		}
	}
	for _, h := range hints {
		if h.country == "IN" {
			return "IN"
		}
	}
	if len(hints) > 0 {
		return hints[0].country
	}
	return ""
}

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}
