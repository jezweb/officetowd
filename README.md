# officetowd

Local ⇄ Office Town worker bisync daemon. Lets you edit the Office Town wiki + binary attachments (PDFs, images) as plain files on your laptop — Obsidian, VSCode, Finder, Spotlight all work.

## Architecture

```
   ~/Documents/my-town/          officetowd            Office Town worker
   ├── contacts/                 (this binary)         (HTTP via MCP bearer)
   ├── orgs/             ⇄  fsnotify + SQLite  ⇄   /api/sync/object/*  ⇄  R2 + D1
   ├── projects/             manifest + bisync         (audit + frontmatter
   └── ...                                              repair + indexing)
```

**No R2 token needed**. The daemon talks to the Office Town worker via the MCP bearer; the worker handles all R2 access via its bindings. One credential boundary for the whole system. Multi-machine writes serialise through the worker.

## Install

Three options — pick one:

### (A) Run the installer from your worker

Open `<your-worker-url>/dashboard/wire-sync` and copy the one-line shell command. The script is shown verbatim on the page — read before running.

### (B) Homebrew (macOS / Linux)

```bash
brew tap jezweb/tap
brew install officetowd
officetowd configure --from-dashboard https://<your-worker>.workers.dev
officetowd start
```

### (C) Ask your AI agent

Copy the agent prompt from `<your-worker-url>/dashboard/wire-sync` Option C. Paste into Claude Code / Goose / Aider — it'll install via brew tap + configure + start.

## CLI

```
officetowd version
officetowd configure [--from-dashboard URL]
officetowd sync                    # one-off bisync pass
officetowd start                   # daemon loop (fsnotify + 60s ticker)
officetowd status                  # manifest stats + config summary
officetowd resync                  # drop manifest + force clean bisync
```

## How it works

- **Watch** local folder with fsnotify
- **Reconcile** on startup + on every change: list worker's objects, walk local tree, diff against SQLite manifest
- **Push** local-newer files via HTTP PUT to worker (worker writes to R2 + D1 + audit + queue indexing + frontmatter AI-repair if needed)
- **Pull** worker-newer files via HTTP GET
- **Conflict** — both sides changed → write remote bytes as `<path>.conflict-<timestamp>` sibling, upload local as authoritative
- **Parallel** apply ops at concurrency 8 — initial sync of thousands of files is roughly 8× faster than serial

Manifest lives at `~/.officetowd/state.db` (SQLite, WAL journal). Pure Go via `modernc.org/sqlite` — no CGO toolchain required, cross-compiles cleanly.

## Configuration

Written by `officetowd configure` to `~/.officetowd/config.yaml` (mode 0600):

```yaml
worker_url: https://office-town-<you>.<account>.workers.dev
bearer: <your MCP bearer — same one used for the dashboard + MCPs>
local_dir: ~/Documents/my-town
prefix: wiki/                    # optional — empty = sync everything
interval_seconds: 60
```

## Status

**v0.2.0+ — shipped.**

End-to-end verified against the demo-town worker (2026-05-28):
- [x] Push markdown with valid + broken + no-frontmatter (worker AI-repairs broken YAML)
- [x] Push binary PNG / PDF
- [x] Pull (worker write detected on periodic sweep)
- [x] Conflict resolution (`.conflict-<ts>` sibling)
- [x] Delete propagation (local rm → remote delete + D1 cleanup)
- [x] Parallel apply ops at concurrency 8
- [x] curl-pipe-bash installer downloads from GH Releases + installs to ~/.local/bin
- [x] 5 platform binaries published (darwin arm64/amd64, linux arm64/amd64, windows amd64)
- [x] Tests pass

Not yet:
- [ ] Optimistic concurrency (server-side `If-Match` + 409)
- [ ] Cross-platform smoke beyond macOS arm64
- [ ] Bulk-import wizard

## Why HTTP-via-worker, not direct R2

Earlier versions of officetowd talked directly to R2 with an S3 client. v0.2.0 pivoted to going through the Office Town worker. Reasons:

- **Zero R2 credential setup for the user** — the worker has `env.WIKI` + `env.FILES` bindings; the daemon only needs the MCP bearer
- **All writes audit-logged centrally** — the worker logs every PUT/DELETE in `wiki_audit`
- **Frontmatter repair on the way through** — broken YAML gets fixed by Workers AI before storage
- **Multi-machine writes serialise** — the worker is the chokepoint
- **No aws-sdk-go-v2** — 30% smaller binary, no dependency tree pulling in 30+ AWS sub-packages

Full architecture note: [unified-write-path-2026-05-28.md](https://github.com/jezweb/office-town-cloud/blob/main/.jez/artifacts/unified-write-path-2026-05-28.md) in office-town-cloud.

## License

MIT. © 2026 Jezweb Pty Ltd.
