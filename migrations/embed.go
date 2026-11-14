// Package migrations embeds the goose SQL migrations so the scraper binary
// can migrate a database without needing the goose CLI installed.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
