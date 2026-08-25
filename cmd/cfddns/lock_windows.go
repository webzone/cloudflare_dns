//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// acquireLock guards against overlapping runs. Windows has no flock, so a
// create-exclusive lock file in the temp dir is used; the file is removed on
// release so a stale file only persists after a hard kill.
func acquireLock() (func(), error) {
	path := filepath.Join(os.TempDir(), "cfddns.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another cfddns run is in progress")
		}
		return nil, fmt.Errorf("open lock: %w", err)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() {
		_ = f.Close()
		_ = os.Remove(path)
	}, nil
}
