// Package config loads and validates runtime configuration from environment
// variables. Deployment uses a systemd EnvironmentFile; local dev uses a .env
// exported into the shell.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the fully validated runtime configuration.
type Config struct {
	// Cloudflare auth. Prefer an API Token; API Key+Email is supported as a
	// dev fallback until the account is migrated to a scoped token.
	CloudflareToken string
	CloudflareEmail string
	CloudflareKey   string

	// DBPath is the SQLite mirror file (standalone; no external database).
	DBPath string

	LogLevel LogLevel
	DryRun   bool
}

func getenv(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// envFileCandidates returns the env files to overlay, in priority order.
// Deployment uses systemd EnvironmentFile; manual runs get the same values
// from the first existing file here.
func envFileCandidates() []string {
	var candidates []string
	if f := getenv("CFDDNS_ENV_FILE"); f != "" {
		candidates = append(candidates, f)
	}
	candidates = append(candidates,
		".env",
		"/etc/cfddns/cfddns.env",
		filepath.Join(os.Getenv("HOME"), ".cfddns.env"),
	)
	// Windows: %USERPROFILE%\.cfddns.env (HOME is usually unset there).
	if p := os.Getenv("USERPROFILE"); p != "" {
		candidates = append(candidates, filepath.Join(p, ".cfddns.env"))
	}
	return candidates
}

// loadEnvFiles overlays KEY=VALUE lines from an env file into the process
// environment without replacing variables the caller already set. Comments
// and blanks are skipped; values may be single- or double-quoted.
func loadEnvFiles() error {
	for _, path := range envFileCandidates() {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Explicitly requested file must be readable.
			if getenv("CFDDNS_ENV_FILE") == path {
				return fmt.Errorf("read env file %s: %w", path, err)
			}
			continue
		}
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			raw := strings.TrimSpace(sc.Text())
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}
			raw = strings.TrimPrefix(raw, "export ")
			eq := strings.IndexByte(raw, '=')
			if eq <= 0 {
				continue
			}
			key := strings.TrimSpace(raw[:eq])
			val := strings.TrimSpace(raw[eq+1:])
			if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
				val = val[1 : len(val)-1]
			}
			if key == "" {
				continue
			}
			if os.Getenv(key) == "" {
				if err := os.Setenv(key, val); err != nil {
					f.Close()
					return fmt.Errorf("setenv %s from %s: %w", key, path, err)
				}
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("parse env file %s: %w", path, err)
		}
		break // first readable file wins
	}
	return nil
}

// Load reads configuration from the environment, after overlaying values from
// an env file (see loadEnvFiles). Unknown/missing required values are fatal so
// a mis-configured cron run fails fast instead of half-running against the
// wrong state.
func Load() (*Config, error) {
	if err := loadEnvFiles(); err != nil {
		return nil, err
	}

	c := &Config{
		CloudflareToken: getenv("CLOUDFLARE_API_TOKEN"),
		CloudflareEmail: getenv("CLOUDFLARE_API_EMAIL"),
		CloudflareKey:   getenv("CLOUDFLARE_API_KEY"),
		DBPath:          getenv("CFDDNS_DB"),
	}
	if c.DBPath == "" {
		// Standalone default: alongside the working directory.
		c.DBPath = "cfddns.db"
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

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.CloudflareToken == "" && (c.CloudflareEmail == "" || c.CloudflareKey == "") {
		return fmt.Errorf("missing Cloudflare auth: set CLOUDFLARE_API_TOKEN (preferred) or CLOUDFLARE_API_EMAIL+CLOUDFLARE_API_KEY")
	}
	return nil
}

// CheckDB validates the mirror database path is set.
func (c *Config) CheckDB() []string {
	if c.DBPath == "" {
		return []string{"CFDDNS_DB"}
	}
	return nil
}

// DBLocation returns the SQLite mirror file path.
func (c *Config) DBLocation() string { return c.DBPath }

type LogLevel string

const (
	Debug LogLevel = "debug"
	Info  LogLevel = "info"
	Warn  LogLevel = "warn"
	Error LogLevel = "error"
)
