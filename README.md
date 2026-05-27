# officetowd

Local⇄R2 bisync daemon for [Office Town](https://github.com/jezweb/office-town) wikis. Modelled on Goanna's `goannad`.

## Why

Office Town's wiki lives in Cloudflare R2 by default — agents access it via MCP, dashboard shows it in browser. But users who want the wiki **on every machine, in Finder, in their editor** need a local mirror.

We tried Syncthing (too slow per Jez's test). We tried `rclone mount` / `mountpoint-s3` (too fragile on macOS, FUSE mount issues). So we're building a purpose-built daemon following the Goanna pattern.

## Install

```bash
brew install jezweb/tap/officetowd
officetowd configure   # interactive — fills ~/.config/officetowd/config.yaml
officetowd start
```

Then your wiki is at `~/Documents/<town>/` and bisyncs continuously to R2 in the background.

## How it works

- **Watch** `<town-path>/` with fsnotify (Linux/macOS) / ReadDirectoryChangesW (Windows)
- **Reconcile** on startup + on every change: diff local mtimes against R2 ETags
- **Push** local-newer files to R2 (S3 PUT)
- **Pull** R2-newer files locally (S3 GET)
- **Conflict** keep both — local gets `.conflict-<timestamp>` suffix + audit log row
- **Notify** the worker on push so it re-indexes (`POST /api/internal/notify-changed`)

Manifest of last-known ETags + mtimes stored in `~/Documents/<town>/.officetowd/manifest.db` (SQLite).

## Status

**v0.1.0 — alpha.** The bisync engine, fsnotify watcher, SQLite manifest, R2 client, and CLI surface all build and pass unit tests against the local-walk + manifest paths.

Tested:
- [x] Repo scaffold + go.mod + cobra CLI entry point
- [x] fsnotify watcher with debounce + recursive dir tracking + ignore patterns
- [x] SQLite manifest (path → hash + mtime + etag, WAL journal mode)
- [x] AWS SDK S3 client against R2 endpoint (list, head, get, put, delete)
- [x] Bisync engine — three-way compare (local + remote + manifest), upload/download/delete/conflict handling
- [x] CLI: `configure`, `sync`, `start` (daemon), `status`, `resync`, `version`
- [x] Unit tests for local walk + manifest roundtrip

Not yet tested:
- [ ] End-to-end against a real R2 bucket (needs your credentials)
- [ ] Conflict resolution under concurrent edits
- [ ] Worker re-index notification on push
- [ ] Multi-machine convergence test (2+ machines syncing same bucket)
- [ ] Performance on a real-world wiki (1000+ files)
- [ ] Homebrew formula + GitHub Releases CI
- [ ] Cross-platform smoke (macOS arm64 only so far)

## Configuration

`~/.config/officetowd/config.yaml`:

```yaml
town: my-town
local_path: ~/Documents/my-town
r2:
  account_id: "<your CF account id>"
  bucket: office-town-substrate
  access_key_id: "<R2 access key>"
  secret_access_key: "<R2 secret>"
  endpoint: https://<account>.r2.cloudflarestorage.com
sync:
  interval_seconds: 5
  ignore:
    - "*.swp"
    - ".DS_Store"
    - "node_modules/"
notify:
  worker_url: https://office-town-<you>.<account>.workers.dev
  auth_token: "<MCP_BEARER_TOKEN>"
```

## CLI

```
officetowd start [--foreground]
officetowd stop
officetowd status
officetowd logs [--follow]
officetowd configure
officetowd resync               # force full bisync from scratch
officetowd push <path>          # one-off push
officetowd pull <path>          # one-off pull
```

## Spec

Full design at:
https://github.com/jezweb/office-town-cloud/blob/main/.jez/artifacts/officetowd-spec-2026-05-28.md

## License

MIT. (c) 2026 Jezweb Pty Ltd.
