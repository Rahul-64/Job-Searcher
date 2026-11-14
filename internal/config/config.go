// Package config loads runtime configuration from environment variables.
// For local development it also reads a .env file if one exists; real
// environment variables always win over the file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// DatabaseURL is the Neon connection string (pooled is fine for the API).
	DatabaseURL string
	// DatabaseURLDirect is the non-pooled Neon URL; preferred for migrations.
	// Falls back to DatabaseURL when empty.
	DatabaseURLDirect string
	// UserAgent identifies the scraper to websites.
	UserAgent string
	// MinInterval is the minimum gap between requests to one host.
	MinInterval time.Duration
	// MaxFailures is how many consecutive failed runs disable a source.
	MaxFailures int
}

// Load reads .env (if present) and then the environment.
func Load() (*Config, error) {
	if err := loadDotEnv(findDotEnv()); err != nil {
		return nil, err
	}
	c := &Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		DatabaseURLDirect: os.Getenv("DATABASE_URL_DIRECT"),
		UserAgent:         envOr("USER_AGENT", "jobscraper/0.1 (personal project)"),
	}
	var err error
	if c.MinInterval, err = envDuration("SCRAPE_MIN_INTERVAL", time.Second); err != nil {
		return nil, err
	}
	if c.MaxFailures, err = envInt("SCRAPE_MAX_FAILURES", 5); err != nil {
		return nil, err
	}
	return c, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s=%q is not a valid duration (examples: 1s, 500ms)", key, v)
	}
	return d, nil
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q must be a positive integer", key, v)
	}
	return n, nil
}

// MigrationURL returns the URL migrations should use.
func (c *Config) MigrationURL() (string, error) {
	if c.DatabaseURLDirect != "" {
		return c.DatabaseURLDirect, nil
	}
	if c.DatabaseURL != "" {
		return c.DatabaseURL, nil
	}
	return "", errors.New("DATABASE_URL is not set (copy .env.example to .env and fill it in)")
}

// RequireDatabaseURL returns DatabaseURL or an error explaining how to set it.
func (c *Config) RequireDatabaseURL() (string, error) {
	if c.DatabaseURL == "" {
		return "", errors.New("DATABASE_URL is not set (copy .env.example to .env and fill it in)")
	}
	return c.DatabaseURL, nil
}

// findDotEnv looks for .env in the working directory, then in each parent up to
// and including the module root (the directory holding go.mod). This lets
// `go test` in a package folder find the project's .env. If nothing is found it
// returns ".env" so a missing file is simply ignored.
func findDotEnv() string {
	dir, err := os.Getwd()
	if err != nil {
		return ".env"
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			return filepath.Join(dir, ".env")
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return ".env" // reached the module root without finding one
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ".env"
		}
		dir = parent
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadDotEnv sets variables from a KEY=VALUE file without overriding variables
// that are already set. A missing file is not an error.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Neon URLs are long; allow generous lines.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
