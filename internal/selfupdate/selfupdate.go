// Package selfupdate keeps the running daemon current with the latest GitHub
// release, so safety/data-loss fixes reach already-installed users without them
// re-running the installer.
//
// Flow: query releases/latest → compare to the build-injected version → if
// newer, download the platform asset, verify its sha256 against the release's
// checksums.txt, smoke-check that the new binary runs, atomically swap it
// (keeping a .bak), and let the caller re-exec. If the binary lives somewhere
// the daemon can't write (root-owned /usr/local/bin), Apply returns a
// *NotWritableError so the caller can fall back to a notify-and-instruct
// message instead of failing silently.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const repo = "jezweb/officetowd"

// Release is the subset of a GitHub release we care about.
type Release struct {
	Tag    string
	Assets map[string]string // asset name → browser_download_url
}

// NotWritableError signals the running binary can't be replaced in place
// (e.g. it's in a root-owned dir). The caller should notify the user with
// manual update instructions rather than treat this as a hard failure.
type NotWritableError struct{ Path string }

func (e *NotWritableError) Error() string { return "binary location not writable: " + e.Path }

// Latest fetches the most recent published release.
func Latest(ctx context.Context) (*Release, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "officetowd-selfupdate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases/latest: HTTP %d", resp.StatusCode)
	}
	var data struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	rel := &Release{Tag: data.TagName, Assets: make(map[string]string, len(data.Assets))}
	for _, a := range data.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// IsNewer reports whether latest is a higher version than current. A "dev" or
// unparseable current is treated as older (so a real release supersedes it).
func IsNewer(current, latest string) bool {
	if strings.TrimSpace(current) == strings.TrimSpace(latest) {
		return false
	}
	cu := parseSemver(current)
	la := parseSemver(latest)
	if la == nil {
		return false // can't understand the remote version — don't update
	}
	if cu == nil {
		return true // local "dev"/unknown — a real release is newer
	}
	for i := 0; i < 3; i++ {
		if la[i] != cu[i] {
			return la[i] > cu[i]
		}
	}
	return false
}

func parseSemver(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i] // drop pre-release / build metadata
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// AssetName is the release asset for the current platform.
func AssetName() string {
	return fmt.Sprintf("officetowd-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

// Apply downloads + verifies + installs the release over the running binary.
// On success it returns the path that was replaced; the caller should re-exec.
func Apply(ctx context.Context, rel *Release) (string, error) {
	name := AssetName()
	tarURL, ok := rel.Assets[name]
	if !ok {
		return "", fmt.Errorf("no release asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if !dirWritable(filepath.Dir(self)) {
		return "", &NotWritableError{Path: self}
	}

	tarBytes, err := download(ctx, tarURL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}

	// Verify against checksums.txt when present (it always is on >= v0.2.4).
	if csURL, ok := rel.Assets["checksums.txt"]; ok {
		csBytes, err := download(ctx, csURL)
		if err != nil {
			return "", fmt.Errorf("download checksums: %w", err)
		}
		want := checksumFor(string(csBytes), name)
		if want == "" {
			return "", fmt.Errorf("checksums.txt has no entry for %s", name)
		}
		if got := sha256hex(tarBytes); got != want {
			return "", fmt.Errorf("checksum mismatch for %s (got %s, want %s)", name, got, want)
		}
	}

	bin, err := extractBinary(tarBytes)
	if err != nil {
		return "", err
	}

	tmp := self + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	if err := smokeCheck(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("new binary failed smoke check: %w", err)
	}

	bak := self + ".bak"
	if err := os.Rename(self, bak); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("backup current binary: %w", err)
	}
	if err := os.Rename(tmp, self); err != nil {
		_ = os.Rename(bak, self) // restore
		_ = os.Remove(tmp)
		return "", fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Remove(bak)
	return self, nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "officetowd-selfupdate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// checksumFor parses sha256sum output ("<hex>  <filename>") and returns the
// hash for the named file, or "".
func checksumFor(checksums, name string) string {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0]
		}
	}
	return ""
}

func extractBinary(tarGz []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(tarGz)))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		base := filepath.Base(hdr.Name)
		if base == "officetowd" || base == "officetowd.exe" {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("no officetowd binary in archive")
}

func smokeCheck(ctx context.Context, path string) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("version produced no output")
	}
	return nil
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".officetowd-wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
