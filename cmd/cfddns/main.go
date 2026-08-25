// Command cfddns is the Cloudflare DNS mirror and dynamic-DNS tool.
// Cloudflare is the single source of truth; a MariaDB mirror is kept in line
// with it so local tooling can read DNS state without hitting the API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/webzone/cloudflare_dns/internal/cli"
	"github.com/webzone/cloudflare_dns/internal/config"
)

func main() { os.Exit(run()) }

func run() int {
	unlock, err := acquireLock()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lock:", err)
		return 2
	}
	defer unlock()

	// Help and no-args work without any configuration.
	if len(os.Args) == 1 {
		fmt.Print(cli.Usage)
		return 0
	}
	for _, a := range os.Args[1:] {
		if a == "help" || a == "--help" || a == "-h" {
			fmt.Print(cli.Usage)
			return 0
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 2
	}
	log := newLogger(cfg.LogLevel)
	app := cli.New(cfg, log)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := app.Run(ctx, os.Args[1:]); err != nil {
		log.Error("fatal", "err", err)
		return 1
	}
	return 0
}

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

func newLogger(level config.LogLevel) *slog.Logger {
	var l slog.Level
	switch level {
	case config.Debug:
		l = slog.LevelDebug
	case config.Info:
		l = slog.LevelInfo
	case config.Warn:
		l = slog.LevelWarn
	case config.Error:
		l = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
