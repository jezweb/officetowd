// officetowd — local⇄worker bisync daemon for Office Town wikis.
//
// Subcommands: configure, start, sync, status, resync, version.
//
// Architecture: all writes flow through the Office Town worker via
// /api/sync/*. The daemon doesn't talk to R2 directly — the worker
// proxies all R2 ops via its bindings, audits every change, and runs
// frontmatter repair on the way through. The user never sees an R2
// token; auth is just the MCP bearer.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jezweb/officetowd/internal/client"
	"github.com/jezweb/officetowd/internal/config"
	"github.com/jezweb/officetowd/internal/manifest"
	"github.com/jezweb/officetowd/internal/selfupdate"
	syncpkg "github.com/jezweb/officetowd/internal/sync"
	"github.com/jezweb/officetowd/internal/watcher"

	"github.com/spf13/cobra"
)

// version is injected at build time via -ldflags "-X main.version=<tag>".
// Defaults to "dev" for local builds (which self-update treats as always-stale).
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "officetowd",
		Short: "Office Town sync daemon — local ↔ worker bisync",
		Long: "Watches a local folder for changes and bisyncs to the Office Town " +
			"worker's /api/sync/* endpoints. The worker handles R2 + audit + " +
			"frontmatter repair + indexing. Daemon's job is filesystem ↔ HTTP.",
	}

	root.AddCommand(cmdVersion())
	root.AddCommand(cmdConfigure())
	root.AddCommand(cmdStart())
	root.AddCommand(cmdSync())
	root.AddCommand(cmdStatus())
	root.AddCommand(cmdResync())
	root.AddCommand(cmdUpdate())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print officetowd version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}
}

func cmdUpdate() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Check for a newer release and install it",
		RunE: func(cmd *cobra.Command, args []string) error {
			runAutoUpdate(cmd.Context(), true)
			return nil
		},
	}
}

// runAutoUpdate checks for a newer release and, if found, installs it and
// re-execs the daemon with the same args. Never fatal: network errors,
// permission issues, and bad downloads all degrade to a log line and false.
// Returns true only if an update was attempted (it normally re-execs and never
// returns). manual=true forces the check even for "dev" builds / opt-out.
func runAutoUpdate(ctx context.Context, manual bool) bool {
	if !manual {
		if version == "dev" {
			return false // local build — don't clobber it
		}
		if os.Getenv("OFFICETOWD_NO_AUTOUPDATE") != "" {
			return false
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	rel, err := selfupdate.Latest(cctx)
	if err != nil {
		if manual {
			fmt.Fprintln(os.Stderr, "update check failed:", err)
		}
		return false
	}
	if !selfupdate.IsNewer(version, rel.Tag) {
		if manual {
			fmt.Printf("officetowd is up to date (%s).\n", version)
		}
		return false
	}

	fmt.Printf("officetowd: update available %s → %s, installing...\n", version, rel.Tag)
	path, err := selfupdate.Apply(cctx, rel)
	if err != nil {
		var nw *selfupdate.NotWritableError
		if errors.As(err, &nw) {
			fmt.Printf("officetowd: %s is newer but I can't replace %s (no write permission).\n", rel.Tag, nw.Path)
			fmt.Println("  Update manually by re-running the installer one-liner from <worker>/dashboard/connect")
		} else {
			fmt.Fprintf(os.Stderr, "officetowd: auto-update to %s failed: %v (staying on %s)\n", rel.Tag, err, version)
		}
		return false
	}

	if manual {
		// One-shot `update`: don't re-exec (the new binary may have a different
		// CLI). Just report; the user's daemon, if running, will reload on its
		// next restart or daily check.
		fmt.Printf("officetowd: updated to %s. Restart 'officetowd start' to use it.\n", rel.Tag)
		return true
	}

	fmt.Printf("officetowd: updated to %s, restarting...\n", rel.Tag)
	// Daemon path: re-exec the freshly-installed binary with the same args + env
	// so the long-running process picks up the new code. 'start' exists in every
	// version, so re-execing it is safe.
	if err := syscall.Exec(path, os.Args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "officetowd: updated but couldn't restart automatically (%v) — please restart officetowd.\n", err)
		os.Exit(0)
	}
	return true
}

func cmdConfigure() *cobra.Command {
	c := &cobra.Command{
		Use:   "configure",
		Short: "Interactive config setup — writes ~/.officetowd/config.yaml",
	}
	var fromDashboard string
	c.Flags().StringVar(&fromDashboard, "from-dashboard", "", "Worker URL to fetch defaults from (e.g. https://my.office-town.workers.dev)")

	c.RunE = func(cmd *cobra.Command, args []string) error {
		reader := bufio.NewReader(os.Stdin)
		ask := func(prompt, def string) string {
			if def != "" {
				fmt.Printf("%s [%s]: ", prompt, def)
			} else {
				fmt.Printf("%s: ", prompt)
			}
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(line)
			if line == "" {
				return def
			}
			return line
		}

		fmt.Println("officetowd configure")
		fmt.Println("====================")
		fmt.Println()
		fmt.Println("You'll need:")
		fmt.Println("  1. Your Office Town worker URL")
		fmt.Println("  2. Your MCP bearer token (visible at <worker>/dashboard/connect)")
		fmt.Println()

		cfg := &config.Config{
			IntervalSeconds: 60,
		}

		cfg.WorkerURL = ask("Worker URL", fromDashboard)

		// If we have a URL, ping /api/sync/credentials to sanity-check
		// before asking for the bearer.
		if cfg.WorkerURL != "" {
			pingURL := strings.TrimRight(cfg.WorkerURL, "/") + "/health"
			req, _ := http.NewRequest(http.MethodGet, pingURL, nil)
			req.Header.Set("User-Agent", "officetowd/"+version)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Printf("  ! Couldn't reach %s — check the URL: %v\n", pingURL, err)
			} else {
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					fmt.Printf("  ! %s returned %d — check the URL\n", pingURL, resp.StatusCode)
				} else {
					fmt.Printf("  ✓ %s reachable\n", pingURL)
				}
			}
		}

		cfg.Bearer = ask("MCP bearer token", "")
		cfg.LocalDir = ask("Local folder to bisync", "~/Documents/my-town")
		cfg.Prefix = ask("Path prefix in worker (e.g. wiki/ for wiki only, empty for everything)", "wiki/")

		if err := cfg.Validate(); err != nil {
			return err
		}
		path, err := config.DefaultPath()
		if err != nil {
			return err
		}
		if err := config.Save(cfg, path); err != nil {
			return err
		}
		fmt.Printf("\n✓ Config written to %s (mode 0600).\n", path)
		fmt.Println()
		fmt.Println("Next:")
		fmt.Println("  officetowd sync     # one-off sync to verify")
		fmt.Println("  officetowd start    # run the daemon in foreground")
		return nil
	}
	return c
}

// loadAll loads config + opens manifest + builds HTTP client. Used by
// start/sync/status/resync.
func loadAll() (*config.Config, *manifest.DB, *client.Client, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := manifest.Open("")
	if err != nil {
		return cfg, nil, nil, err
	}
	machineID, _ := os.Hostname()
	cl := client.New(cfg.WorkerURL, cfg.Bearer, machineID)
	return cfg, m, cl, nil
}

func cmdSync() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Run one bisync pass + exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, mfn, cl, err := loadAll()
			if err != nil {
				return err
			}
			defer mfn.Close()
			eng := &syncpkg.Engine{
				LocalDir:       cfg.LocalDir,
				Prefix:         cfg.Prefix,
				Client:         cl,
				Manifest:       mfn,
				IgnorePrefixes: []string{".git", ".officetowd", "node_modules", ".DS_Store"},
				Logf:           func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
			}
			stats, err := eng.Sync(ctx)
			if err != nil {
				return err
			}
			fmt.Println()
			fmt.Println(stats.String())
			return nil
		},
	}
}

func cmdStart() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the bisync daemon (foreground)",
		Long:  "Runs an initial sync, then watches for local changes + periodic remote sweeps. Ctrl-C to stop.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			// Self-update before doing anything else, so a daemon left running
			// for weeks still picks up safety fixes. Re-execs on success.
			runAutoUpdate(ctx, false)

			cfg, mfn, cl, err := loadAll()
			if err != nil {
				return err
			}
			defer mfn.Close()

			eng := &syncpkg.Engine{
				LocalDir:       cfg.LocalDir,
				Prefix:         cfg.Prefix,
				Client:         cl,
				Manifest:       mfn,
				IgnorePrefixes: []string{".git", ".officetowd", "node_modules", ".DS_Store"},
				Logf:           func(f string, a ...any) { fmt.Printf("[sync] "+f+"\n", a...) },
			}

			// Initial sync.
			fmt.Println("officetowd: running initial sync...")
			if stats, err := eng.Sync(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "initial sync error:", err)
			} else {
				fmt.Println("[sync]", stats.String())
			}

			// Set up watcher.
			w, err := watcher.New(cfg.LocalDir, watcher.Options{})
			if err != nil {
				return fmt.Errorf("watcher: %w", err)
			}
			defer w.Stop()

			// Goroutine for fsnotify.
			go func() {
				if err := w.Start(ctx); err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "watcher exited:", err)
				}
			}()

			// Periodic ticker — catches remote-side changes we can't observe locally.
			ticker := time.NewTicker(time.Duration(cfg.IntervalSeconds) * time.Second)
			defer ticker.Stop()

			// Daily self-update check for long-running daemons. Re-execs on success.
			updateTicker := time.NewTicker(24 * time.Hour)
			defer updateTicker.Stop()

			fmt.Printf("officetowd: watching %s ↔ %s (prefix %q, interval %ds)\n",
				cfg.LocalDir, cfg.WorkerURL, cfg.Prefix, cfg.IntervalSeconds)
			fmt.Println("officetowd: Ctrl-C to stop")

			for {
				select {
				case <-ctx.Done():
					fmt.Println("\nofficetowd: shutting down")
					return nil
				case <-updateTicker.C:
					runAutoUpdate(ctx, false)
				case <-w.Changes():
					if stats, err := eng.Sync(ctx); err != nil {
						fmt.Fprintln(os.Stderr, "[sync] error:", err)
					} else if stats.Uploaded+stats.Downloaded+stats.DeletedLoc+stats.DeletedRem+stats.Conflicts > 0 {
						fmt.Println("[sync]", stats.String())
					}
				case <-ticker.C:
					if stats, err := eng.Sync(ctx); err != nil {
						fmt.Fprintln(os.Stderr, "[sync] error:", err)
					} else if stats.Uploaded+stats.Downloaded+stats.DeletedLoc+stats.DeletedRem+stats.Conflicts > 0 {
						fmt.Println("[sync]", stats.String())
					}
				}
			}
		},
	}
}

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show manifest stats + config summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, mfn, _, err := loadAll()
			if err != nil {
				return err
			}
			defer mfn.Close()
			entries, err := mfn.List(cfg.Prefix)
			if err != nil {
				return err
			}
			fmt.Printf("Worker:      %s\n", cfg.WorkerURL)
			fmt.Printf("Local dir:   %s\n", cfg.LocalDir)
			fmt.Printf("Prefix:      %q\n", cfg.Prefix)
			fmt.Printf("Manifest:    %d entries\n", len(entries))
			if len(entries) > 0 {
				latest := entries[0].LastSyncedAt
				for _, e := range entries {
					if e.LastSyncedAt.After(latest) {
						latest = e.LastSyncedAt
					}
				}
				fmt.Printf("Last sync:   %s\n", latest.Format(time.RFC3339))
			}
			return nil
		},
	}
}

func cmdResync() *cobra.Command {
	return &cobra.Command{
		Use:   "resync",
		Short: "Drop the manifest and force a clean bisync",
		Long:  "Use when the manifest is suspected to be out of sync with reality (e.g. you manually deleted state.db).",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := manifest.DefaultPath()
			if err != nil {
				return err
			}
			fmt.Printf("Removing manifest at %s\n", path)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("Running fresh sync...")
			return cmdSync().RunE(cmd, args)
		},
	}
}
