// Package config loads and saves the officetowd configuration.
//
// Config lives at ~/.officetowd/config.yaml by default. It records:
//   - WorkerURL: the Office Town worker that proxies to R2
//   - Bearer:    the MCP bearer token (same one used for the dashboard)
//   - LocalDir:  the local folder to bisync
//   - Prefix:    optional path prefix in the worker (e.g. "wiki/")
//
// No R2 credentials — the worker handles all R2 access via its bindings.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk shape.
type Config struct {
	// WorkerURL is the Office Town worker base URL, e.g.
	//   https://office-town.jezweb.workers.dev
	// No trailing slash.
	WorkerURL string `yaml:"worker_url"`

	// Bearer is the MCP bearer token — same as the one used to wire
	// MCPs into Goose. Visible at <worker>/dashboard/connect.
	Bearer string `yaml:"bearer"`

	// LocalDir is the local folder to bisync (e.g. ~/Documents/my-town).
	// Stored as an absolute path; the loader expands ~/ on read.
	LocalDir string `yaml:"local_dir"`

	// Prefix limits the sync to a subtree of the worker's keyspace
	// (e.g. "wiki/" for just the wiki bucket). Empty = whole worker
	// (wiki + files).
	Prefix string `yaml:"prefix"`

	// IntervalSeconds is how often to do a passive sweep in addition
	// to fsnotify-triggered syncs. 0 disables periodic scans.
	IntervalSeconds int `yaml:"interval_seconds"`
}

// DefaultPath returns ~/.officetowd/config.yaml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".officetowd", "config.yaml"), nil
}

// Load reads the config from disk. Returns an error if the file is missing
// or malformed; the caller is expected to direct the user to `officetowd
// configure` in that case.
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config not found at %s — run `officetowd configure` first", path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Expand ~/ in LocalDir for ergonomics.
	if expanded, ok := expandHome(c.LocalDir); ok {
		c.LocalDir = expanded
	}
	if c.IntervalSeconds == 0 {
		c.IntervalSeconds = 60
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config to disk at file mode 0600 (rw owner only).
// Creates the parent directory if missing.
func Save(c *Config, path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Validate returns an error if required fields are missing.
func (c *Config) Validate() error {
	if c.WorkerURL == "" {
		return errors.New("worker_url is required (e.g. https://office-town.<account>.workers.dev)")
	}
	if c.Bearer == "" {
		return errors.New("bearer is required (visible at <worker>/dashboard/connect)")
	}
	if c.LocalDir == "" {
		return errors.New("local_dir is required")
	}
	return nil
}

// expandHome replaces a leading ~/ with the user's home dir.
func expandHome(path string) (string, bool) {
	if len(path) < 2 || path[:2] != "~/" {
		return path, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path, false
	}
	return filepath.Join(home, path[2:]), true
}
