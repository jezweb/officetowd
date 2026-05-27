// Package config loads and saves the officetowd configuration.
//
// The config lives at ~/.officetowd/config.yaml by default. It records the
// R2 endpoint + access keys, the local town folder, and the bucket name.
// Secrets are stored in the same file with file mode 0600; we don't
// integrate with system keychains in v1 to keep the install story simple.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk shape. Fields tagged for YAML.
type Config struct {
	// Endpoint is the S3-compatible R2 endpoint, e.g.
	//   https://<account-id>.r2.cloudflarestorage.com
	Endpoint string `yaml:"endpoint"`

	// AccessKeyID + SecretAccessKey are an R2 token scoped to the bucket.
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`

	// Bucket is the R2 bucket name (e.g. "office-town-wiki").
	Bucket string `yaml:"bucket"`

	// LocalDir is the local folder to bisync (e.g. ~/Documents/my-town).
	// Stored as an absolute path; the loader expands ~/ on read.
	LocalDir string `yaml:"local_dir"`

	// Prefix limits the sync to a subtree of the bucket (e.g. "wiki/").
	// Empty string syncs the whole bucket.
	Prefix string `yaml:"prefix"`

	// IntervalSeconds is how often to do a passive scan in addition to
	// fsnotify-triggered syncs. 0 disables periodic scans.
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

// Validate returns an error if required fields are missing or obviously
// wrong. Called by Load; callers building a Config from scratch should
// call this themselves before Save.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return errors.New("endpoint is required (e.g. https://<account-id>.r2.cloudflarestorage.com)")
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return errors.New("access_key_id and secret_access_key are required")
	}
	if c.Bucket == "" {
		return errors.New("bucket is required")
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
