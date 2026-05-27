// Local-walk + manifest sanity test. Doesn't hit R2 — exercises the
// walkLocal path against a temp dir to confirm hashing and filtering.
package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jezweb/officetowd/internal/manifest"
)

func TestWalkLocalIgnoresExpected(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "wiki", "contacts", "alice.md"), "alice\n")
	mustWrite(t, filepath.Join(tmp, "wiki", "contacts", ".DS_Store"), "garbage")
	mustWrite(t, filepath.Join(tmp, ".git", "config"), "git config")
	mustWrite(t, filepath.Join(tmp, "node_modules", "pkg", "index.js"), "module.exports = 1")
	mustWrite(t, filepath.Join(tmp, "wiki", "contacts", "alice.md.conflict-20260528-120000"), "conflict copy")
	mustWrite(t, filepath.Join(tmp, "README.md.swp"), "vim swp")

	e := &Engine{
		LocalDir:       tmp,
		IgnorePrefixes: []string{".git", ".officetowd", "node_modules", ".DS_Store"},
	}
	got, err := e.walkLocal()
	if err != nil {
		t.Fatalf("walkLocal: %v", err)
	}
	if _, ok := got["wiki/contacts/alice.md"]; !ok {
		t.Errorf("expected wiki/contacts/alice.md in walk result, got: %v", keys(got))
	}
	// Things that should NOT show up:
	for _, banned := range []string{
		"wiki/contacts/.DS_Store",
		".git/config",
		"node_modules/pkg/index.js",
		"README.md.swp",
	} {
		if _, ok := got[banned]; ok {
			t.Errorf("walk should have skipped %q but included it", banned)
		}
	}
	// Conflict siblings: skipped by name pattern.
	for k := range got {
		if strings.Contains(k, ".conflict-") {
			t.Errorf("walk included a .conflict- file: %s", k)
		}
	}
}

func TestManifestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	m, err := manifest.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer m.Close()
	e := &manifest.Entry{
		Path:       "wiki/contacts/alice.md",
		LocalHash:  "abc123",
		LocalSize:  42,
		RemoteETag: "deadbeef",
		RemoteSize: 42,
	}
	if err := m.Put(e); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := m.Get(e.Path)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("entry not found after put")
	}
	if got.LocalHash != e.LocalHash {
		t.Errorf("hash mismatch: got %q want %q", got.LocalHash, e.LocalHash)
	}
	if err := m.Delete(e.Path); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := m.Get(e.Path); got != nil {
		t.Errorf("entry still exists after delete: %+v", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
