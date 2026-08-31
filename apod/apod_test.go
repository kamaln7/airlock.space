package apod

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactAPIKey(t *testing.T) {
	err := errors.New(`Get "https://api.nasa.gov/planetary/apod?api_key=HZ33hRbsecret123": context deadline exceeded`)
	got := redactAPIKey(err).Error()
	if strings.Contains(got, "secret123") {
		t.Fatalf("api key leaked: %s", got)
	}
	if !strings.Contains(got, "api_key=REDACTED") {
		t.Fatalf("expected redaction marker: %s", got)
	}
}
