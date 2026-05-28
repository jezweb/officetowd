// Package sync is the bisync engine.
//
// Strategy — three-way compare per path:
//
//   for each path that exists in local-walk ∪ remote-listing ∪ manifest:
//     localState  := has-changed-since-manifest?(local-walk[path])
//     remoteState := has-changed-since-manifest?(remote-listing[path])
//
//     match (local-existed, remote-existed, localChanged, remoteChanged):
//       case (no,  no,  -,    -)    → drop from manifest
//       case (yes, no,  no,   -)    → upload to remote (it was deleted there)
//       case (yes, no,  yes,  -)    → upload to remote (local-only change)
//       case (no,  yes, -,    no)   → delete local (we already had it, remote
//                                     unchanged means we deleted; if we never
//                                     had it, download)
//       case (no,  yes, -,    yes)  → download to local (new remote)
//       case (yes, yes, no,   no)   → noop
//       case (yes, yes, yes,  no)   → upload (local newer)
//       case (yes, yes, no,   yes)  → download (remote newer)
//       case (yes, yes, yes,  yes)  → CONFLICT: keep both
//                                       — write remote bytes to <path>.conflict-<ts>
//                                       — upload local bytes as authoritative
//                                       — record both in manifest
//
// Last-write-wins via timestamp comparison is a fallback when the manifest
// doesn't have an entry (first-time sync). Real conflict resolution requires
// the manifest's previous-known state on both sides — which is why the
// manifest exists.
package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jezweb/officetowd/internal/client"
	"github.com/jezweb/officetowd/internal/manifest"
)

// Engine runs bisync cycles.
type Engine struct {
	LocalDir string
	Prefix   string // optional — empty string means whole bucket
	Client   *client.Client
	Manifest *manifest.DB

	// IgnorePrefixes mirrors the watcher's ignore set so we don't sync
	// .git, node_modules, .officetowd, etc.
	IgnorePrefixes []string

	// Logf is called with progress messages. nil → no logging.
	Logf func(format string, args ...any)
}

func (e *Engine) log(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

// Sync runs one full bisync pass.
func (e *Engine) Sync(ctx context.Context) (Stats, error) {
	var stats Stats

	localFiles, err := e.walkLocal()
	if err != nil {
		return stats, fmt.Errorf("walk local: %w", err)
	}
	e.log("local: %d files", len(localFiles))

	remoteObjs, err := e.Client.List(ctx, e.Prefix)
	if err != nil {
		return stats, fmt.Errorf("list remote: %w", err)
	}
	remote := make(map[string]client.Object, len(remoteObjs))
	for _, o := range remoteObjs {
		rel := strings.TrimPrefix(o.Key, e.Prefix)
		remote[rel] = o
	}
	e.log("remote: %d objects", len(remote))

	// All paths we know about: local ∪ remote ∪ manifest.
	allPaths := make(map[string]struct{})
	for p := range localFiles {
		allPaths[p] = struct{}{}
	}
	for p := range remote {
		allPaths[p] = struct{}{}
	}
	manifestPaths, err := e.Manifest.AllPaths()
	if err != nil {
		return stats, fmt.Errorf("manifest paths: %w", err)
	}
	for p := range manifestPaths {
		allPaths[p] = struct{}{}
	}

	for path := range allPaths {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		action, err := e.decide(ctx, path, localFiles, remote)
		if err != nil {
			e.log("error deciding for %s: %v", path, err)
			stats.Errors++
			continue
		}
		if err := e.apply(ctx, action); err != nil {
			e.log("error applying %s for %s: %v", action.Op, path, err)
			stats.Errors++
			continue
		}
		stats.bump(action.Op)
	}
	return stats, nil
}

// Stats counts the operations of one sync pass.
type Stats struct {
	Uploaded   int
	Downloaded int
	DeletedLoc int
	DeletedRem int
	Conflicts  int
	Noop       int
	Errors     int
}

func (s *Stats) bump(op opCode) {
	switch op {
	case opUpload:
		s.Uploaded++
	case opDownload:
		s.Downloaded++
	case opDeleteLocal:
		s.DeletedLoc++
	case opDeleteRemote:
		s.DeletedRem++
	case opConflict:
		s.Conflicts++
	case opNoop:
		s.Noop++
	}
}

// String returns a human one-liner.
func (s Stats) String() string {
	return fmt.Sprintf(
		"uploaded=%d downloaded=%d del-local=%d del-remote=%d conflicts=%d noop=%d errors=%d",
		s.Uploaded, s.Downloaded, s.DeletedLoc, s.DeletedRem, s.Conflicts, s.Noop, s.Errors)
}

// ---------- decision + apply ----------

type opCode int

const (
	opNoop opCode = iota
	opUpload
	opDownload
	opDeleteLocal
	opDeleteRemote
	opConflict
)

type action struct {
	Path string
	Op   opCode

	// Cached state to avoid re-stat / re-head during apply.
	LocalInfo  *localFile
	RemoteInfo *client.Object
	ManEntry   *manifest.Entry
}

func (e *Engine) decide(ctx context.Context, path string, localFiles map[string]*localFile, remote map[string]client.Object) (action, error) {
	a := action{Path: path}
	loc, locOK := localFiles[path]
	rem, remOK := remote[path]
	man, err := e.Manifest.Get(path)
	if err != nil {
		return a, err
	}
	a.LocalInfo = loc
	if remOK {
		r := rem
		a.RemoteInfo = &r
	}
	a.ManEntry = man

	localChanged := locOK && (man == nil || loc.Hash != man.LocalHash)
	remoteChanged := remOK && (man == nil || rem.ETag != man.RemoteETag)

	switch {
	case !locOK && !remOK:
		// Both sides gone — clean up the manifest.
		if err := e.Manifest.Delete(path); err != nil {
			return a, err
		}
		a.Op = opNoop
		return a, nil

	case locOK && !remOK:
		// Local has it, remote doesn't.
		if man != nil && man.RemoteETag != "" && !localChanged {
			// We had it on the remote, didn't change it locally, but it's
			// now gone from remote → remote was deleted by someone else.
			// Delete it locally to match.
			a.Op = opDeleteLocal
			return a, nil
		}
		// Either we never had it remotely, or we changed it locally.
		// Upload either way.
		a.Op = opUpload
		return a, nil

	case !locOK && remOK:
		if man != nil && man.LocalHash != "" && !remoteChanged {
			// We had it locally + on remote, deleted it locally, remote
			// unchanged → delete from remote to match.
			a.Op = opDeleteRemote
			return a, nil
		}
		// Either we never had it locally, or remote was modified.
		// Download either way.
		a.Op = opDownload
		return a, nil

	case localChanged && remoteChanged:
		a.Op = opConflict
		return a, nil

	case localChanged:
		a.Op = opUpload
		return a, nil

	case remoteChanged:
		a.Op = opDownload
		return a, nil

	default:
		a.Op = opNoop
		return a, nil
	}
}

func (e *Engine) apply(ctx context.Context, a action) error {
	switch a.Op {
	case opNoop:
		return nil

	case opUpload:
		return e.doUpload(ctx, a)

	case opDownload:
		return e.doDownload(ctx, a)

	case opDeleteLocal:
		return e.doDeleteLocal(a)

	case opDeleteRemote:
		return e.doDeleteRemote(ctx, a)

	case opConflict:
		return e.doConflict(ctx, a)
	}
	return fmt.Errorf("unknown op: %v", a.Op)
}

func (e *Engine) doUpload(ctx context.Context, a action) error {
	body, err := os.ReadFile(filepath.Join(e.LocalDir, a.Path))
	if err != nil {
		return fmt.Errorf("read local: %w", err)
	}
	reason := "filesystem-sync upload"
	pr, err := e.Client.Put(ctx, e.Prefix+a.Path, body, reason)
	if err != nil {
		return err
	}
	if pr.Repaired {
		e.log("UP   %s (%d B, etag %s) — server repaired frontmatter: %s", a.Path, len(body), pr.ETag, pr.RepairNote)
	} else {
		e.log("UP   %s (%d B, etag %s)", a.Path, len(body), pr.ETag)
	}
	return e.Manifest.Put(&manifest.Entry{
		Path:           a.Path,
		LocalHash:      a.LocalInfo.Hash,
		LocalMtime:     a.LocalInfo.Mtime,
		LocalSize:      a.LocalInfo.Size,
		RemoteETag:     pr.ETag,
		RemoteModified: time.Now(),
		RemoteSize:     pr.Size,
		LastSyncedAt:   time.Now(),
	})
}

func (e *Engine) doDownload(ctx context.Context, a action) error {
	body, obj, err := e.Client.Get(ctx, e.Prefix+a.Path)
	if err != nil {
		return err
	}
	dest := filepath.Join(e.LocalDir, a.Path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir for download: %w", err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("write local: %w", err)
	}
	hash := hashBytes(body)
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("stat after write: %w", err)
	}
	e.log("DOWN %s (%d B)", a.Path, len(body))
	return e.Manifest.Put(&manifest.Entry{
		Path:           a.Path,
		LocalHash:      hash,
		LocalMtime:     info.ModTime(),
		LocalSize:      int64(len(body)),
		RemoteETag:     obj.ETag,
		RemoteModified: obj.LastModified,
		RemoteSize:     obj.Size,
		LastSyncedAt:   time.Now(),
	})
}

func (e *Engine) doDeleteLocal(a action) error {
	dest := filepath.Join(e.LocalDir, a.Path)
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove local: %w", err)
	}
	e.log("DEL  %s (local)", a.Path)
	return e.Manifest.Delete(a.Path)
}

func (e *Engine) doDeleteRemote(ctx context.Context, a action) error {
	if err := e.Client.Delete(ctx, e.Prefix+a.Path, "filesystem-sync delete"); err != nil {
		return err
	}
	e.log("DEL  %s (remote)", a.Path)
	return e.Manifest.Delete(a.Path)
}

func (e *Engine) doConflict(ctx context.Context, a action) error {
	// True three-way conflict: both sides changed. We don't try to merge.
	// Strategy:
	//   1. Read the remote bytes.
	//   2. Write them to <path>.conflict-<unix-ts> locally.
	//   3. Upload the (still untouched) local bytes as the authoritative
	//      copy. The user gets both files and can resolve manually.
	remoteBody, _, err := e.Client.Get(ctx, e.Prefix+a.Path)
	if err != nil {
		return fmt.Errorf("fetch remote for conflict: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	conflictPath := filepath.Join(e.LocalDir, a.Path+".conflict-"+stamp)
	if err := os.MkdirAll(filepath.Dir(conflictPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for conflict: %w", err)
	}
	if err := os.WriteFile(conflictPath, remoteBody, 0o644); err != nil {
		return fmt.Errorf("write conflict file: %w", err)
	}
	e.log("CONF %s — saved remote as .conflict-%s, uploading local", a.Path, stamp)
	// Now upload the local copy as authoritative.
	uploadAction := action{
		Path:       a.Path,
		Op:         opUpload,
		LocalInfo:  a.LocalInfo,
		RemoteInfo: a.RemoteInfo,
		ManEntry:   a.ManEntry,
	}
	return e.doUpload(ctx, uploadAction)
}

// ---------- local walk ----------

type localFile struct {
	Hash  string
	Mtime time.Time
	Size  int64
}

// walkLocal returns every file under LocalDir as a map: rel-path → state.
// Hashes everything on the spot — cheap for typical wiki sizes (<100MB).
// If wikis grow to multi-GB this should be optimised to skip-hash-if-mtime-and-size-match-manifest.
func (e *Engine) walkLocal() (map[string]*localFile, error) {
	out := make(map[string]*localFile)
	root := e.LocalDir
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			for _, ig := range e.IgnorePrefixes {
				if rel == ig || strings.HasPrefix(rel, ig+"/") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		// Skip conflict siblings — they live alongside real entries and
		// only exist for the user to resolve. We never sync them.
		base := filepath.Base(p)
		if strings.Contains(base, ".conflict-") {
			return nil
		}
		// Skip transient editor files.
		if strings.HasPrefix(base, ".#") || strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".swp") || base == ".DS_Store" {
			return nil
		}

		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		out[rel] = &localFile{
			Hash:  hashBytes(body),
			Mtime: info.ModTime(),
			Size:  info.Size(),
		}
		return nil
	})
	return out, err
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// IsContentEqual is a helper for tests + the conflict path: two byte
// slices are "really equal" when their bodies match. We don't trust
// ETag-vs-hash comparison alone because R2 ETags for non-multipart
// uploads are md5, but multipart uploads use an opaque format.
func IsContentEqual(a, b []byte) bool { return bytes.Equal(a, b) }
