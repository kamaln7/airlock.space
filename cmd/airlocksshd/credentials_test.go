package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNASAAPIKey(t *testing.T) {
	tests := []struct {
		name, nasaKey, nasaAPIKey, file, want string
	}{
		{"credential", "", "", "  credential-key\n", "credential-key"},
		{"env wins", "environment-key", "", "credential-key\n", "environment-key"},
		{"NASA_API_KEY wins", "", "from-api", "credential-key\n", ""},
		{"absent file", "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NASAKEY", tt.nasaKey)
			t.Setenv("NASA_API_KEY", tt.nasaAPIKey)
			dir := t.TempDir()
			t.Setenv("CREDENTIALS_DIRECTORY", dir)
			if tt.file != "" {
				if err := os.WriteFile(filepath.Join(dir, nasaAPIKeyCredential), []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := loadNASAAPIKey(); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv("NASAKEY"); got != tt.want {
				t.Errorf("NASAKEY = %q; want %q", got, tt.want)
			}
		})
	}
}
