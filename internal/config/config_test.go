package config

import (
	"os"
	"path/filepath"
	"testing"
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
