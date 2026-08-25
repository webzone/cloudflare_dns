// Command cfddns is the Cloudflare DNS mirror and dynamic-DNS tool.
// Cloudflare is the single source of truth; a MariaDB mirror is kept in line
// with it so local tooling can read DNS state without hitting the API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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

	// Help, version and no-args work without any configuration.
	if len(os.Args) == 1 {
		fmt.Print(cli.Usage)
		return 0
	}
	for _, a := range os.Args[1:] {
		switch a {
		case "help", "--help", "-h":
			fmt.Print(cli.Usage)
			return 0
		case "version", "--version", "-V":
			fmt.Println(cli.Version)
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
