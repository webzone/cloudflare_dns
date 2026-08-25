// Package config reads runtime configuration from environment variables with
// sane built-in defaults, so no external config file is needed. The Cloudflare
// API token is managed by cfddns itself (see TokenFilePath); everything else
// has a working default.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the fully validated runtime configuration.
type Config struct {
	// Cloudflare auth. Prefer the API token (env or `cfddns token set`);
	// the legacy API Key+Email is still accepted.
	CloudflareToken string
	CloudflareEmail string
	CloudflareKey   string

	// DBPath is the SQLite database file (standalone; no external database).
	DBPath string

	LogLevel LogLevel
	DryRun   bool
}

func getenv(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// userConfigDir returns the per-user cfddns directory
// ($CFDDNS_CONFIG_DIR override, or ~/.cfddns; %USERPROFILE%\.cfddns on
// Windows).
func userConfigDir() string {
	if p := getenv("CFDDNS_CONFIG_DIR"); p != "" {
		return p
	}
	if p := getenv("USERPROFILE"); p != "" {
		return filepath.Join(p, ".cfddns")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cfddns")
	}
	return ".cfddns"
}

// DefaultDBPath is where the SQLite database lives when CFDDNS_DB is unset:
// next to the stored token in the per-user cfddns directory.
func DefaultDBPath() string {
	return filepath.Join(userConfigDir(), "cfddns.db")
}

// Load reads configuration from the environment, filling in defaults.
func Load() (*Config, error) {
	c := &Config{
		CloudflareToken: getenv("CLOUDFLARE_API_TOKEN"),
		CloudflareEmail: getenv("CLOUDFLARE_API_EMAIL"),
		CloudflareKey:   getenv("CLOUDFLARE_API_KEY"),
		DBPath:          getenv("CFDDNS_DB"),
	}
	if c.DBPath == "" {
		c.DBPath = DefaultDBPath()
	}
	// Token store fallback: when no token is in the environment, use the
	// stored one (managed via `cfddns token set`).
	if c.CloudflareToken == "" {
		tok, err := LoadTokenFile()
		if err != nil {
			return nil, err
		}
		c.CloudflareToken = tok
	}

	lvl := strings.ToLower(getenv("LOG_LEVEL"))
	if lvl == "" {
		lvl = "info"
	}
	switch lvl {
	case "debug", "info", "warn", "error":
		c.LogLevel = LogLevel(lvl)
	default:
		return nil, fmt.Errorf("invalid LOG_LEVEL %q (want debug|info|warn|error)", lvl)
	}

	dry := strings.ToLower(getenv("CF_DDNS_DRY_RUN"))
	c.DryRun = dry == "1" || dry == "true" || dry == "yes"

	return c, nil
}

// CheckDB validates the database path is set.
func (c *Config) CheckDB() []string {
	if c.DBPath == "" {
		return []string{"CFDDNS_DB"}
	}
	return nil
}

// DBLocation returns the SQLite database file path.
func (c *Config) DBLocation() string { return c.DBPath }

type LogLevel string

const (
	Debug LogLevel = "debug"
	Info  LogLevel = "info"
	Warn  LogLevel = "warn"
	Error LogLevel = "error"
)
