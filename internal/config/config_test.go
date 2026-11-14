package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationURLPrefersDirect(t *testing.T) {
	c := &Config{DatabaseURL: "pooled", DatabaseURLDirect: "direct"}
	got, err := c.MigrationURL()
	if err != nil || got != "direct" {
		t.Fatalf("got %q, %v; want direct", got, err)
	}
	c = &Config{DatabaseURL: "pooled"}
	got, err = c.MigrationURL()
	if err != nil || got != "pooled" {
		t.Fatalf("got %q, %v; want pooled", got, err)
	}
	if _, err := (&Config{}).MigrationURL(); err == nil {
		t.Fatal("expected error when no URL is set")
	}
}

func TestLoadDotEnvDoesNotOverrideEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nA_TEST_KEY=from_file\nB_TEST_KEY=\"quoted value\"\n\nBAD LINE\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A_TEST_KEY", "from_env")
	os.Unsetenv("B_TEST_KEY")
	t.Cleanup(func() { os.Unsetenv("B_TEST_KEY") })

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("A_TEST_KEY"); got != "from_env" {
		t.Errorf("A_TEST_KEY = %q, want from_env (env must win)", got)
	}
	if got := os.Getenv("B_TEST_KEY"); got != "quoted value" {
		t.Errorf("B_TEST_KEY = %q, want %q", got, "quoted value")
	}
}

func TestLoadDotEnvMissingFileIsOK(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Setenv("SCRAPE_MIN_INTERVAL", "")
	t.Setenv("SCRAPE_MAX_FAILURES", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MinInterval != time.Second || c.MaxFailures != 5 {
		t.Errorf("defaults wrong: %v, %d", c.MinInterval, c.MaxFailures)
	}

	t.Setenv("SCRAPE_MIN_INTERVAL", "2500ms")
	t.Setenv("SCRAPE_MAX_FAILURES", "9")
	c, err = Load()
	if err != nil || c.MinInterval != 2500*time.Millisecond || c.MaxFailures != 9 {
		t.Errorf("overrides wrong: %+v, %v", c, err)
	}

	for _, bad := range []struct{ k, v string }{
		{"SCRAPE_MIN_INTERVAL", "fast"}, {"SCRAPE_MIN_INTERVAL", "-1s"},
		{"SCRAPE_MAX_FAILURES", "0"}, {"SCRAPE_MAX_FAILURES", "many"},
	} {
		t.Setenv("SCRAPE_MIN_INTERVAL", "")
		t.Setenv("SCRAPE_MAX_FAILURES", "")
		t.Setenv(bad.k, bad.v)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%q should be rejected", bad.k, bad.v)
		}
	}
}
