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

**Pre-alpha — scaffold only.** v1.1 deliverable per `office-town-cloud/.jez/artifacts/V1.1-PLAN-2026-05-28.md` Phase 2.1. Full implementation: ~1-2 weeks of focused Go work.

Tracked work:
- [x] Repo scaffold + go.mod + cobra CLI entry point
- [ ] fsnotify watcher
- [ ] SQLite manifest
- [ ] AWS SDK S3 client for R2
- [ ] Bisync algorithm + conflict resolution
- [ ] Worker notify-changed webhook
- [ ] Homebrew formula + GitHub Releases CI
- [ ] Cross-platform testing (macOS arm64+amd64, Linux amd64+arm64, Windows amd64)

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
