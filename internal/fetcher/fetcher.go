// Package fetcher defines the contract every job source adapter implements.
// A new source is a new adapter package plus one row in the sources table;
// the rest of the pipeline does not change.
package fetcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ErrBoardNotFound is returned when a source token does not exist (HTTP 404).
var ErrBoardNotFound = errors.New("board not found")

// RawJob is one job exactly as a source reported it, lightly structured.
type RawJob struct {
	ExternalID      string // the source's own job id
	URL             string // public link to the posting
	Title           string
	Location        string // free text, e.g. "Bengaluru-VTP, India"
	Department      string
	DescriptionHTML string     // HTML, already entity-decoded
	PostedAt        *time.Time // first published
	UpdatedAt       *time.Time
	Payload         []byte // original JSON for this job (stored compressed)
}

// ContentHash fingerprints the parts of a job whose change matters.
// Identical hash across runs means "unchanged".
func (j RawJob) ContentHash() string {
	h := sha256.New()
	upd := ""
	if j.UpdatedAt != nil {
		upd = j.UpdatedAt.UTC().Format(time.RFC3339)
	}
	for _, p := range []string{j.Title, j.Location, j.Department, j.DescriptionHTML, upd} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Adapter fetches jobs from one kind of source (greenhouse, lever, ...).
type Adapter interface {
	// Kind is the value stored in sources.kind.
	Kind() string
	// BoardURL is the canonical API URL for a board token (stored in sources.base_url).
	BoardURL(token string) string
	// Company returns the human-readable company name for a board token.
	Company(ctx context.Context, token string) (string, error)
	// Fetch returns every job currently listed on the board.
	Fetch(ctx context.Context, token string) ([]RawJob, error)
}
