// Package identity gives this machine a stable, random device id. It is the
// ONLY identity the daemon carries — a coin it flips once, not anything read
// about the user. The worker derives everything else useful (timezone, region)
// from the connection. See office-town CONCEPT-the-workflow.
package identity

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeviceID returns this machine's device id, minting + persisting one on first
// call (~/.officetowd/device_id, 0600). Stable across runs.
func DeviceID() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".officetowd")
	path := filepath.Join(dir, "device_id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// newID returns a random UUIDv4 string (no external dependency).
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
