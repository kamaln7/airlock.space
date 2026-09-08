package apod

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPIKeyFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("env wins", func(t *testing.T) {
		t.Setenv("NASA_API_KEY", "from-env")
		t.Setenv("NASA_API_KEY_PATH", path)
		got, err := apiKeyFromEnv()
		if err != nil || got != "from-env" {
			t.Fatalf("got %q, %v; want from-env", got, err)
		}
	})
	t.Run("path", func(t *testing.T) {
		t.Setenv("NASA_API_KEY", "")
		t.Setenv("NASA_API_KEY_PATH", path)
		got, err := apiKeyFromEnv()
		if err != nil || got != "from-file" {
			t.Fatalf("got %q, %v; want from-file", got, err)
		}
	})
	t.Run("neither", func(t *testing.T) {
		t.Setenv("NASA_API_KEY", "")
		t.Setenv("NASA_API_KEY_PATH", "")
		got, err := apiKeyFromEnv()
		if err != nil || got != "" {
			t.Fatalf("got %q, %v; want empty", got, err)
		}
	})
	t.Run("missing path", func(t *testing.T) {
		t.Setenv("NASA_API_KEY", "")
		t.Setenv("NASA_API_KEY_PATH", filepath.Join(dir, "missing"))
		if _, err := apiKeyFromEnv(); err == nil {
			t.Fatal("want error")
		}
	})
}
