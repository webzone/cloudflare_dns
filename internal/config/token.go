package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenFilePath returns where cfddns stores its Cloudflare API token:
// $CFDDNS_TOKEN_FILE, else <user config dir>/token.
func TokenFilePath() string {
	if p := getenv("CFDDNS_TOKEN_FILE"); p != "" {
		return p
	}
	return filepath.Join(userConfigDir(), "token")
}

// LoadTokenFile returns the stored token ("" when none is stored).
func LoadTokenFile() (string, error) {
	b, err := os.ReadFile(TokenFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read token file %s: %w", TokenFilePath(), err)
	}
	return strings.TrimSpace(string(b)), nil
}

// SaveTokenFile writes the token with owner-only permissions.
func SaveTokenFile(token string) error {
	path := TokenFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write token file %s: %w", path, err)
	}
	return nil
}

// RemoveTokenFile deletes the stored token (no-op when none is stored).
func RemoveTokenFile() error {
	path := TokenFilePath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token file %s: %w", path, err)
	}
	return nil
}

// HasTokenFile reports whether a token file exists.
func HasTokenFile() bool {
	_, err := os.Stat(TokenFilePath())
	return err == nil
}
