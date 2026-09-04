package bucket

import (
	"os"
	"strings"
)

// Environment variables FromEnv reads. HANZO_API_BASE is the name other
// Hanzo products already use for this same gateway; this package does not
// invent its own.
const (
	// EnvBaseURL is the Hanzo API base. Default: https://api.hanzo.ai
	EnvBaseURL = "HANZO_API_BASE"

	// EnvToken is the caller's Hanzo IAM bearer token. The operator wires
	// this from a KMS-backed secret; it is NEVER stored in plaintext in the
	// repo or image.
	EnvToken = "HANZO_API_TOKEN"
)

// DefaultBaseURL is the public Hanzo API gateway.
const DefaultBaseURL = "https://api.hanzo.ai"

// ConfigFromEnv builds a Config from HANZO_API_BASE and HANZO_API_TOKEN.
func ConfigFromEnv() Config {
	base := strings.TrimSpace(os.Getenv(EnvBaseURL))
	if base == "" {
		base = DefaultBaseURL
	}
	return Config{
		BaseURL: base,
		Token:   strings.TrimSpace(os.Getenv(EnvToken)),
	}
}

// FromEnv builds a Client from HANZO_API_BASE and HANZO_API_TOKEN — the
// one-liner products use at startup:
//
//	client, _ := bucket.FromEnv()
func FromEnv() (*Client, error) {
	return New(ConfigFromEnv())
}
