package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok", "token")
	t.Setenv("CFDDNS_TOKEN_FILE", path)

	if got, err := LoadTokenFile(); err != nil || got != "" {
		t.Fatalf("empty store: got %q err %v", got, err)
	}
	if HasTokenFile() {
		t.Fatal("no file should exist before save")
	}

	if err := SaveTokenFile("cfut_abc123\n"); err != nil {
		t.Fatal(err)
	}
	if !HasTokenFile() {
		t.Fatal("file should exist after save")
	}
	got, err := LoadTokenFile()
	if err != nil || got != "cfut_abc123" {
		t.Fatalf("load after save = %q err %v", got, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file perms = %v, want 0600", fi.Mode().Perm())
	}

	if err := RemoveTokenFile(); err != nil {
		t.Fatal(err)
	}
	if HasTokenFile() {
		t.Fatal("file should be gone after remove")
	}
	if err := RemoveTokenFile(); err != nil {
		t.Fatalf("second remove should be a no-op, got %v", err)
	}
}
