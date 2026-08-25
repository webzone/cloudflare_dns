//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// acquireLock guards against overlapping runs (e.g. a systemd timer firing
// while a previous invocation is still running). Advisory flock only.
func acquireLock() (func(), error) {
	f, err := os.OpenFile("/tmp/cfddns.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another cfddns run is in progress")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
