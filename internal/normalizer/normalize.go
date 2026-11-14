package normalizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Version identifies the rules in this package. Bump it whenever a change would
// alter any derived field; `scraper renormalize` then re-derives stored jobs
// from their raw payloads, without re-scraping.
// v2: " or " separates locations, "<descriptor> US" resolves to the country, more macro regions.
const Version = 3

// Input is everything the normalizer may use about one posting.
type Input struct {
	CompanyID   int64
	Title       string
	Location    string
	Description string // plain text
	Department  string

	// Structured hints the source itself provided; all optional.
	SourceCountry    string // ISO alpha-2
	SourceRemote     string // "remote" | "hybrid" | "onsite" in the source's wording
	SourceEmployment string
	SourceSalary     *Salary
}

// Output holds the derived fields stored on the canonical job.
type Output struct {
	TitleNormalized string
	Seniority       string
	EmploymentType  string
	RemoteType      string
	Places          []Place // primary first
	ExpMin, ExpMax  *int
	Salary          *Salary
	Fingerprint     string
	Version         int
}

// Primary returns the first place, or a zero Place.
func (o Output) Primary() Place {
	if len(o.Places) > 0 {
		return o.Places[0]
	}
	return Place{}
}

// Normalize derives every cleaned field from one posting.
func Normalize(in Input) Output {
	out := Output{Version: Version}

	out.TitleNormalized = NormalizeTitle(in.Title)
	out.Seniority = Seniority(out.TitleNormalized)

	loc := ParseLocation(in.Location)
	out.Places = loc.Places
	if len(out.Places) == 0 && in.SourceCountry != "" {
		// The text said nothing usable but the source states a country.
		// SourceCountry is documented as an ISO code, so it is safe to upper-case
		// (CountryCode itself is strict: a bare lower-case "in" is a word).
		sc := strings.TrimSpace(in.SourceCountry)
		c, ok := CountryCode(strings.ToUpper(sc))
		if !ok {
			c, ok = CountryCode(sc) // a full name such as "Germany"
		}
		if ok {
			out.Places = []Place{{Country: c}}
		}
	}

	out.RemoteType = DetectRemote(in.SourceRemote, loc, in.Title, in.Description)

	out.EmploymentType = NormalizeEmployment(in.SourceEmployment)
	if out.EmploymentType == "" {
		out.EmploymentType = EmploymentFromTitle(out.TitleNormalized)
	}

	out.ExpMin, out.ExpMax = ParseExperience(in.Description)

	if in.SourceSalary != nil && in.SourceSalary.Max > 0 {
		s := *in.SourceSalary
		out.Salary = &s // structured data beats text
	} else {
		out.Salary = ParseSalary(in.Description)
	}

	out.Fingerprint = Fingerprint(in.CompanyID, out.TitleNormalized, out.Primary(), in.Description)
	return out
}

// Fingerprint identifies "the same opening" for dedup: same company, title,
// primary place and description. The description is part of it on purpose:
// real data has many postings with the same title and city but different teams
// and different job descriptions (e.g. nine "Staff Software Engineer,
// Bengaluru" openings with nine descriptions); those are different jobs and
// must not be merged. Truly identical postings (several openings of one role)
// collapse into one canonical job.
func Fingerprint(companyID int64, titleNormalized string, place Place, description string) string {
	desc := strings.ToLower(multiWS.ReplaceAllString(strings.TrimSpace(description), " "))
	dsum := sha256.Sum256([]byte(desc))
	key := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s", companyID, titleNormalized,
		strings.ToLower(place.City), strings.ToLower(place.Region), place.Country, hex.EncodeToString(dsum[:8]))
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
