package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/collector"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		runInit(os.Args[2:])
	case "sync":
		runSync(os.Args[2:])
	case "daemon":
		runDaemon(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "update":
		runUpdate(os.Args[2:])
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Print(`ai-ledger-collector — incremental raw AI log collector

Usage:
  ai-ledger-collector init --server <url> --token <device-token> [--config <path>]
  ai-ledger-collector sync [--config <path>] [--dry-run] [-v|--verbose]
  ai-ledger-collector daemon [--config <path>] [--interval 3m] [-v|--verbose]
  ai-ledger-collector status [--config <path>]
  ai-ledger-collector update [--check] [--force] [--version <tag>] [--no-restart]
  ai-ledger-collector version
`)
}

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", collector.DefaultConfigPath(), "config file path")
	serverURL := fs.String("server", "http://127.0.0.1:8484", "ai-ledger server URL")
	token := fs.String("token", "", "device bearer token from ai-ledger-server token create")
	_ = fs.Parse(args)

	if strings.TrimSpace(*token) == "" {
		fmt.Fprintln(os.Stderr, "error: --token is required")
		os.Exit(2)
	}
	cfg := collector.DefaultConfig()
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(*serverURL), "/")
	cfg.DeviceToken = strings.TrimSpace(*token)

	if err := collector.SaveConfig(*cfgPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "save config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote collector config to %s\n", *cfgPath)
}

func loadConfigOrExit(path string) collector.Config {
	cfg, err := collector.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\nRun 'ai-ledger-collector init --server ... --token ...' first.\n", path, err)
		os.Exit(1)
	}
	if cfg.ServerURL == "" || cfg.DeviceToken == "" {
		fmt.Fprintln(os.Stderr, "error: server_url and device_token must be set in config")
		os.Exit(1)
	}
	return cfg
}

func runSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	cfgPath := fs.String("config", collector.DefaultConfigPath(), "config file path")
	dryRun := fs.Bool("dry-run", false, "discover and report without uploading")
	verbose := fs.Bool("verbose", false, "log each file as it syncs")
	verboseShort := fs.Bool("v", false, "shorthand for --verbose")
	_ = fs.Parse(args)

	cfg := loadConfigOrExit(*cfgPath)
	st, err := collector.LoadState(cfg.StatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load state: %v\n", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	syncer := collector.NewSyncer(cfg, st, logger, *dryRun)
	syncer.SetVerbose(*verbose || *verboseShort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	stats, err := syncer.SyncOnce(ctx)
	if err != nil {
		logger.Error("sync failed", "err", err)
		os.Exit(1)
	}
	logger.Info("sync complete",
		"discovered", stats.Discovered,
		"uploaded_files", stats.UploadedFiles,
		"uploaded_bytes", stats.UploadedBytes,
		"skipped", stats.Skipped,
		"errors", stats.Errors,
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
	if stats.Errors > 0 {
		os.Exit(1)
	}
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	cfgPath := fs.String("config", collector.DefaultConfigPath(), "config file path")
	interval := fs.Duration("interval", 3*time.Minute, "sync interval")
	verbose := fs.Bool("verbose", false, "log each file as it syncs")
	verboseShort := fs.Bool("v", false, "shorthand for --verbose")
	_ = fs.Parse(args)

	cfg := loadConfigOrExit(*cfgPath)
	st, err := collector.LoadState(cfg.StatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load state: %v\n", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	syncer := collector.NewSyncer(cfg, st, logger, false)
	syncer.SetVerbose(*verbose || *verboseShort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runPass := func() {
		stats, err := syncer.SyncOnce(ctx)
		if err != nil {
			logger.Warn("sync pass failed", "err", err)
			return
		}
		if stats.UploadedFiles > 0 || stats.Errors > 0 {
			logger.Info("sync pass",
				"discovered", stats.Discovered,
				"uploaded_files", stats.UploadedFiles,
				"uploaded_bytes", stats.UploadedBytes,
				"errors", stats.Errors,
			)
		}
	}

	logger.Info("ai-ledger-collector daemon started", "server", cfg.ServerURL, "interval", interval.String())
	runPass()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("daemon stopped")
			return
		case <-ticker.C:
			runPass()
		}
	}
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", collector.DefaultConfigPath(), "config file path")
	_ = fs.Parse(args)

	cfg, err := collector.LoadConfig(*cfgPath)
	if err != nil {
		cfg = collector.DefaultConfig()
		fmt.Printf("No config at %s (showing auto-discovered defaults)\n\n", *cfgPath)
	} else {
		fmt.Printf("Config: %s\nServer: %s\nState:  %s\n\n", *cfgPath, cfg.ServerURL, cfg.StatePath)
	}

	files, err := collector.Discover(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		os.Exit(1)
	}
	st, _ := collector.LoadState(cfg.StatePath)

	counts := make(map[string]int)
	bytesBySource := make(map[string]int64)
	syncedBySource := make(map[string]int)
	var totalBytes int64
	for _, f := range files {
		counts[f.Source]++
		bytesBySource[f.Source] += f.Size
		totalBytes += f.Size
		if st != nil {
			if c, ok := st.Get(f.Source, f.RelPath); ok && c.Size == f.Size {
				syncedBySource[f.Source]++
			}
		}
	}

	fmt.Printf("Discovered %d files (%.2f MB total):\n", len(files), float64(totalBytes)/(1024*1024))
	for src, cnt := range counts {
		fmt.Printf("  %-14s %4d files  %8.2f MB  (%d up-to-date)\n", src, cnt, float64(bytesBySource[src])/(1024*1024), syncedBySource[src])
	}
}

func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "check whether a newer release is available without installing")
	force := fs.Bool("force", false, "reinstall even if already on the target version")
	targetVer := fs.String("version", "latest", "target release tag (default: latest)")
	repo := fs.String("repo", collector.DefaultReleaseRepo, "GitHub repository (owner/repo)")
	noRestart := fs.Bool("no-restart", false, "skip restarting background service after updating")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := collector.SelfUpdate(ctx, collector.UpdateOptions{
		Repo:           *repo,
		CurrentVersion: version,
		TargetVersion:  *targetVer,
		CheckOnly:      *checkOnly,
		Force:          *force,
		NoRestart:      *noRestart,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		os.Exit(1)
	}

	if res.AlreadyUpToDate {
		fmt.Printf("ai-ledger-collector is already up to date (%s).\n", res.CurrentVersion)
		return
	}
	if *checkOnly {
		fmt.Printf("Update available: %s -> %s\nRun 'ai-ledger-collector update' to install.\n", res.CurrentVersion, res.LatestVersion)
		return
	}

	fmt.Printf("Updated ai-ledger-collector: %s -> %s (%s)\n", res.CurrentVersion, res.LatestVersion, res.ExecutablePath)
	if res.ChecksumVerified {
		fmt.Println("Verified SHA-256 checksum against SHA256SUMS.txt")
	}
	if res.ServiceNote != "" {
		fmt.Println(res.ServiceNote)
	}
}
