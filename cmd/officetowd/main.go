// officetowd — local⇄R2 bisync daemon for Office Town wikis.
//
// Subcommands: configure, start, sync, pull, push, status, version.
// See ~/Documents/office-town-cloud/.jez/artifacts/officetowd-spec-2026-05-28.md
// for the design.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jezweb/officetowd/internal/config"
	"github.com/jezweb/officetowd/internal/manifest"
	"github.com/jezweb/officetowd/internal/r2"
	syncpkg "github.com/jezweb/officetowd/internal/sync"
	"github.com/jezweb/officetowd/internal/watcher"

	"github.com/spf13/cobra"
)

const version = "0.1.0"

func main() {
	root := &cobra.Command{
		Use:   "officetowd",
		Short: "Local⇄R2 bisync daemon for Office Town wikis",
		Long:  "Watches a local town folder for changes and bisyncs to R2. Goanna-style.",
	}

	root.AddCommand(cmdVersion())
	root.AddCommand(cmdConfigure())
	root.AddCommand(cmdStart())
	root.AddCommand(cmdSync())
	root.AddCommand(cmdStatus())
	root.AddCommand(cmdResync())

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

func cmdConfigure() *cobra.Command {
	return &cobra.Command{
		Use:   "configure",
		Short: "Interactive config setup — writes ~/.officetowd/config.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
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
			fmt.Println("You'll need an R2 token scoped to the bucket. Generate one at")
			fmt.Println("https://dash.cloudflare.com → R2 → Manage API tokens.")
			fmt.Println()

			c := &config.Config{
				IntervalSeconds: 60,
			}

			c.Endpoint = ask("R2 endpoint (https://<account-id>.r2.cloudflarestorage.com)", "")
			c.AccessKeyID = ask("Access Key ID", "")
			c.SecretAccessKey = ask("Secret Access Key", "")
			c.Bucket = ask("Bucket name (e.g. office-town-wiki)", "office-town-wiki")
			c.LocalDir = ask("Local folder to bisync", "~/Documents/my-town")
			c.Prefix = ask("Bucket prefix (optional, '' for whole bucket)", "")

			if err := c.Validate(); err != nil {
				return err
			}
			path, err := config.DefaultPath()
			if err != nil {
				return err
			}
			if err := config.Save(c, path); err != nil {
				return err
			}
			fmt.Printf("\nConfig written to %s (mode 0600).\n", path)
			fmt.Println("Run `officetowd start` to begin syncing.")
			return nil
		},
	}
}

// loadAll loads config + opens manifest + builds R2 client. Used by start/sync/status/resync.
func loadAll(ctx context.Context) (*config.Config, *manifest.DB, *r2.Client, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := manifest.Open("")
	if err != nil {
		return cfg, nil, nil, err
	}
	c, err := r2.New(ctx, cfg.Endpoint, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.Bucket)
	if err != nil {
		_ = m.Close()
		return cfg, nil, nil, err
	}
	return cfg, m, c, nil
}

func cmdSync() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Run one bisync pass + exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, mfn, cl, err := loadAll(ctx)
			if err != nil {
				return err
			}
			defer mfn.Close()
			eng := &syncpkg.Engine{
				LocalDir:       cfg.LocalDir,
				Prefix:         cfg.Prefix,
				R2:             cl,
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

			cfg, mfn, cl, err := loadAll(ctx)
			if err != nil {
				return err
			}
			defer mfn.Close()

			eng := &syncpkg.Engine{
				LocalDir:       cfg.LocalDir,
				Prefix:         cfg.Prefix,
				R2:             cl,
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

			fmt.Printf("officetowd: watching %s ↔ %s/%s (interval %ds)\n",
				cfg.LocalDir, cfg.Bucket, cfg.Prefix, cfg.IntervalSeconds)
			fmt.Println("officetowd: Ctrl-C to stop")

			for {
				select {
				case <-ctx.Done():
					fmt.Println("\nofficetowd: shutting down")
					return nil
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
			ctx := cmd.Context()
			cfg, mfn, _, err := loadAll(ctx)
			if err != nil {
				return err
			}
			defer mfn.Close()
			entries, err := mfn.List(cfg.Prefix)
			if err != nil {
				return err
			}
			fmt.Printf("Local dir:   %s\n", cfg.LocalDir)
			fmt.Printf("Bucket:      %s (prefix %q)\n", cfg.Bucket, cfg.Prefix)
			fmt.Printf("Endpoint:    %s\n", cfg.Endpoint)
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
