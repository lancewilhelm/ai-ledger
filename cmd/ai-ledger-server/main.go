package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata"

	"github.com/lancewilhelm/ai-ledger/internal/archive"
	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/server"
	"github.com/lancewilhelm/ai-ledger/internal/store"
	"github.com/lancewilhelm/ai-ledger/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "serve":
			runServe(os.Args[2:])
			return
		case "token":
			runToken(os.Args[2:])
			return
		case "reparse":
			runReparse(os.Args[2:])
			return
		case "migrate":
			runMigrate(os.Args[2:])
			return
		case "healthcheck":
			runHealthcheck(os.Args[2:])
			return
		case "version":
			fmt.Println(version)
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}
	runServe(os.Args[1:])
}

func printUsage() {
	fmt.Print(`ai-ledger-server — personal AI usage ledger & lossless transcript archive

Usage:
  ai-ledger-server [serve] [--bind 0.0.0.0:8484] [--data ./data]
  ai-ledger-server token create --name <device-name> [--data ./data]
  ai-ledger-server token list [--data ./data]
  ai-ledger-server token revoke --id <device-id-or-name> [--data ./data]
  ai-ledger-server reparse [--data ./data]
  ai-ledger-server migrate [--data ./data]
  ai-ledger-server healthcheck [--url http://127.0.0.1:8484/api/health]
  ai-ledger-server version

Environment variables:
  AI_LEDGER_BIND                   Bind address (default: 0.0.0.0:8484)
  AI_LEDGER_DATA_DIR               Data directory for SQLite DB & raw archive (default: ./data)
  AI_LEDGER_ADMIN_TOKEN            Optional bearer token protecting /api/admin/*
  AI_LEDGER_RATES_FILE             Optional path to custom or T3 usage-model-rates.json
  AI_LEDGER_KEEP_ALL_SNAPSHOTS     Keep every historical snapshot version (default: true)
  AI_LEDGER_SKIP_MIGRATION_BACKUP  Skip automatic pre-migration SQLite backup (default: false)
`)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

func openDeps(dataDir, ratesFile string, keepAll bool) (*store.SQLiteStore, *archive.Store, *pricing.Calculator, error) {
	dbPath := filepath.Join(dataDir, "ai-ledger.sqlite")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	arch, err := archive.New(filepath.Join(dataDir, "archive"), keepAll)
	if err != nil {
		st.Close()
		return nil, nil, nil, fmt.Errorf("open archive: %w", err)
	}
	calc := pricing.NewCalculator()
	if ratesFile != "" {
		if n, err := calc.LoadCustomRatesFile(ratesFile); err != nil {
			slog.Warn("could not load rates file", "path", ratesFile, "err", err)
		} else {
			slog.Info("loaded custom pricing rates", "path", ratesFile, "models", n)
		}
	}
	if overrides, err := st.ListPricingOverrides(context.Background()); err == nil && len(overrides) > 0 {
		entries := make([]pricing.OverrideEntry, 0, len(overrides))
		for _, ov := range overrides {
			entries = append(entries, pricing.OverrideEntry{
				Model:     ov.Model,
				Rate:      pricing.FromPerMillion(ov.InputPerM, ov.CachedInputPerM, ov.CacheWrite5mPerM, ov.CacheWrite1hPerM, ov.OutputPerM),
				Notes:     ov.Notes,
				UpdatedAt: ov.UpdatedAt,
			})
		}
		calc.ReplaceOverrides(entries)
		slog.Info("loaded manual pricing overrides", "models", len(entries))
	}
	return st, arch, calc, nil
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	bind := fs.String("bind", envOr("AI_LEDGER_BIND", "0.0.0.0:8484"), "HTTP bind address")
	dataDir := fs.String("data", envOr("AI_LEDGER_DATA_DIR", "./data"), "data directory")
	adminToken := fs.String("admin-token", envOr("AI_LEDGER_ADMIN_TOKEN", ""), "optional bearer token for /api/admin/*")
	ratesFile := fs.String("rates-file", envOr("AI_LEDGER_RATES_FILE", ""), "optional model rates JSON file")
	keepAll := fs.Bool("keep-all-snapshots", envBool("AI_LEDGER_KEEP_ALL_SNAPSHOTS", true), "preserve every historical snapshot version")
	_ = fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, arch, calc, err := openDeps(*dataDir, *ratesFile, *keepAll)
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Load any previously archived rates JSON files automatically
	if files, err := st.ListAllRawFiles(context.Background()); err == nil {
		for _, f := range files {
			if f.Source == "rates" {
				if n, err := calc.LoadT3RatesFile(arch.AbsPath(f.ArchivePath)); err == nil {
					logger.Info("loaded archived rates file", "file", f.RelPath, "models", n)
				}
			}
		}
	}

	srv := server.New(server.Config{
		BindAddr:   *bind,
		Version:    version,
		AdminToken: *adminToken,
		WebFS:      web.Dist(),
		Logger:     logger,
	}, st, arch, calc)
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              *bind,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		mig := st.MigrationResult()
		logger.Info("ai-ledger-server listening",
			"version", version,
			"bind", *bind,
			"data_dir", *dataDir,
			"schema_version", mig.CurrentVersion,
			"keep_all_snapshots", *keepAll,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}

func runToken(args []string) {
	if len(args) == 0 {
		printUsage()
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("token "+sub, flag.ExitOnError)
	dataDir := fs.String("data", envOr("AI_LEDGER_DATA_DIR", "./data"), "data directory")
	name := fs.String("name", "", "device name (for create)")
	id := fs.String("id", "", "device id or name (for revoke)")
	_ = fs.Parse(args[1:])

	st, _, _, err := openDeps(*dataDir, "", true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx := context.Background()
	switch sub {
	case "create":
		if strings.TrimSpace(*name) == "" {
			fmt.Fprintln(os.Stderr, "error: --name is required")
			os.Exit(2)
		}
		plain, hash, err := server.GenerateToken()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		dev, err := st.CreateDevice(ctx, strings.TrimSpace(*name), "", "", "", hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created device %q (id: %s)\n", dev.Name, dev.ID)
		fmt.Printf("Device token (save now — shown only once):\n  %s\n", plain)

	case "list":
		devs, err := st.ListDevices(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tOS/ARCH\tFILES\tSESSIONS\tLAST SEEN\tSTATUS")
		for _, d := range devs {
			last := "never"
			if d.LastSeenAt != nil {
				last = d.LastSeenAt.Format(time.RFC3339)
			}
			status := "active"
			if d.RevokedAt != nil {
				status = "revoked"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s/%s\t%d\t%d\t%s\t%s\n", d.ID, d.Name, d.OS, d.Arch, d.FileCount, d.SessionCount, last, status)
		}
		tw.Flush()

	case "revoke":
		if strings.TrimSpace(*id) == "" {
			fmt.Fprintln(os.Stderr, "error: --id is required")
			os.Exit(2)
		}
		if err := st.RevokeDevice(ctx, strings.TrimSpace(*id)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Revoked device %q\n", *id)

	default:
		printUsage()
		os.Exit(2)
	}
}

func runReparse(args []string) {
	fs := flag.NewFlagSet("reparse", flag.ExitOnError)
	dataDir := fs.String("data", envOr("AI_LEDGER_DATA_DIR", "./data"), "data directory")
	ratesFile := fs.String("rates-file", envOr("AI_LEDGER_RATES_FILE", ""), "optional model rates JSON file")
	_ = fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, arch, calc, err := openDeps(*dataDir, *ratesFile, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := server.New(server.Config{Logger: logger}, st, arch, calc)
	defer srv.Close()

	start := time.Now()
	parsed, failed, err := srv.ReparseAll(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "reparse error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Reparse complete in %s: parsed=%d failed=%d\n", time.Since(start).Round(time.Millisecond), parsed, failed)
}

func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dataDir := fs.String("data", envOr("AI_LEDGER_DATA_DIR", "./data"), "data directory")
	_ = fs.Parse(args)

	dbPath := filepath.Join(*dataDir, "ai-ledger.sqlite")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	res := st.MigrationResult()
	fmt.Printf("SQLite migrations complete: version=%d applied=%d", res.CurrentVersion, res.Applied)
	if res.BackupPath != "" {
		fmt.Printf(" backup=%s", res.BackupPath)
	}
	fmt.Println()
}

func runHealthcheck(args []string) {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8484/api/health", "health endpoint URL")
	timeout := fs.Duration("timeout", 4*time.Second, "HTTP request timeout")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck returned status %d\n", resp.StatusCode)
		os.Exit(1)
	}
}
