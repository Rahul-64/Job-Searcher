// Package config loads runtime configuration from environment variables.
// For local development it also reads a .env file if one exists; real
// environment variables always win over the file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	// DatabaseURL is the Neon connection string (pooled is fine for the API).
	DatabaseURL string
	// DatabaseURLDirect is the non-pooled Neon URL; preferred for migrations.
	// Falls back to DatabaseURL when empty.
	DatabaseURLDirect string
	// UserAgent identifies the scraper to websites.
	UserAgent string
}

// Load reads .env (if present) and then the environment.
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, err
	}
	c := &Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		DatabaseURLDirect: os.Getenv("DATABASE_URL_DIRECT"),
		UserAgent:         envOr("USER_AGENT", "jobscraper/0.1 (personal project)"),
	}
	return c, nil
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
