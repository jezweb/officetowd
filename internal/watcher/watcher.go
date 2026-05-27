// Package watcher wraps fsnotify in a simpler interface: it emits one
// debounced "something under <root> changed" signal per quiescence window.
// The sync engine reacts to the signal with a full directory walk — we
// don't try to track individual file events because fsnotify under
// rename/replace patterns (vim, editor saves) emits unreliable streams.
package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher emits a signal on Changes() each time the watched tree quiesces
// after activity. Debounce window is configurable via Options.
type Watcher struct {
	root    string
	fsw     *fsnotify.Watcher
	changes chan struct{}
	stop    chan struct{}
	opts    Options
}

// Options tune the watcher.
type Options struct {
	// Debounce is the quiescence window — wait this long after the last
	// event before emitting a Changes() signal. 300ms is a good default
	// (long enough for vim's write-rename dance to finish, short enough
	// that users don't notice the lag).
	Debounce time.Duration

	// IgnorePrefixes is a list of path prefixes (relative to root) we
	// won't recurse into. Default: [".git", ".officetowd", "node_modules"].
	IgnorePrefixes []string
}

// New constructs a Watcher rooted at the given path. Walks the tree once
// and registers every directory with fsnotify (since fsnotify on most
// platforms is non-recursive).
func New(root string, opts Options) (*Watcher, error) {
	if opts.Debounce == 0 {
		opts.Debounce = 300 * time.Millisecond
	}
	if opts.IgnorePrefixes == nil {
		opts.IgnorePrefixes = []string{".git", ".officetowd", "node_modules", ".DS_Store"}
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("new fsnotify: %w", err)
	}
	w := &Watcher{
		root:    root,
		fsw:     fsw,
		changes: make(chan struct{}, 1),
		stop:    make(chan struct{}),
		opts:    opts,
	}
	if err := w.addRecursive(root); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	return w, nil
}

// Changes returns the channel that emits one signal per debounce window.
// Callers should drain it before doing a sync to avoid pile-up.
func (w *Watcher) Changes() <-chan struct{} { return w.changes }

// Start begins processing events in a goroutine. Blocks until ctx is
// cancelled or Stop() is called.
func (w *Watcher) Start(ctx context.Context) error {
	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.stop:
			return nil
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if w.shouldIgnore(ev.Name) {
				continue
			}
			// If a directory was created, start watching it too. Fsnotify
			// is non-recursive on Linux/macOS.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addRecursive(ev.Name)
				}
			}
			// Reset the debounce timer.
			if timer == nil {
				timer = time.AfterFunc(w.opts.Debounce, w.emit)
			} else {
				timer.Reset(w.opts.Debounce)
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			// Fsnotify errors are usually recoverable (rate-limit on
			// macOS, etc.). Log via fmt.Println so the daemon's stdout
			// shows them and don't kill the loop.
			fmt.Println("watcher error:", err)
		}
	}
}

// Stop closes the underlying fsnotify watcher and signals Start to exit.
func (w *Watcher) Stop() error {
	close(w.stop)
	return w.fsw.Close()
}

func (w *Watcher) emit() {
	// Non-blocking send — if the receiver hasn't drained the previous
	// signal yet, we coalesce.
	select {
	case w.changes <- struct{}{}:
	default:
	}
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than abort the whole walk
		}
		if !d.IsDir() {
			return nil
		}
		if w.shouldIgnore(path) {
			return filepath.SkipDir
		}
		return w.fsw.Add(path)
	})
}

func (w *Watcher) shouldIgnore(path string) bool {
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return false
	}
	// Normalise separators for matching.
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) == 0 {
		return false
	}
	for _, ig := range w.opts.IgnorePrefixes {
		if parts[0] == ig {
			return true
		}
	}
	// Always skip dotfiles created by editors.
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".#") || strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".swp") {
		return true
	}
	return false
}
