# UPGRADE.md — Multica Worker Node Upgrade Guide

> **Scope:** Worker nodes (MacBook Pro, MacBook Max) — daemon only.
> Full-stack host (MacBook Air) uses a different process; see the Multica Upgrade skill.
> This file lives in the private fork and is intended for Claude Code on worker nodes.

---

## What a Worker Node Upgrade Is

Worker nodes run the Multica **daemon only** — they do NOT host the server or web UI.
Those live on the Air. So an upgrade here means:

1. Pull latest from the private fork
2. Build CLI binary with version stamp
3. Restart daemon

That's it. No migrations (those run on Air). No server restart. No web build.

---

## Prerequisite — Locate the Multica Checkout

All multica clones live under `~/ai/`. The directory may be `~/ai/multica` or `~/ai/multica-private` depending on when it was set up. Set `MULTICA_REPO` once and reuse it throughout:

```bash
MULTICA_REPO=$(ls -d ~/ai/multica ~/ai/multica-private 2>/dev/null | head -1)
[ -z "$MULTICA_REPO" ] && { echo "FATAL: no multica checkout under ~/ai/" >&2; exit 1; }
echo "Using $MULTICA_REPO"
```

Every step below uses `cd "$MULTICA_REPO"`.

---

## Step 0 — Confirm You're on a Worker Node

```bash
hostname   # should contain MacBook-Pro or MacBook-Max, NOT MacBook-Air
multica version || echo "(baseline unavailable — continue anyway)"
```

If you're on Air by mistake, stop — use the Multica Upgrade skill instead.

A failing baseline (`exit 137`, empty output) usually means a previously-poisoned binary and does NOT block the upgrade — Step 4's `rm`+`cp`+`codesign` will heal it.

---

## Step 1 — Check What Version to Upgrade To

The Air always upgrades first and pushes to the fork. Check what's available:

```bash
cd "$MULTICA_REPO"
git fetch fork
git log HEAD..fork/deploy-private --oneline   # commits we don't have yet
```

The latest commit message on the fork will show the target version (e.g. `chore: bump web version to 0.2.13`).
Alternatively: `python3 -c "import json; print(json.load(open('apps/web/package.json'))['version'])"` reads the current fork version.

---

## Step 2 — Sync to Fork Branch

First check your current branch:

```bash
cd "$MULTICA_REPO"
git branch --show-current
git remote -v | grep fork
```

**If you are already on `deploy-private`** (and no conflicts expected):
```bash
git merge fork/deploy-private
```

**If you are on a different branch** (e.g. `private`, `main`, or anything else) — do NOT merge.
Instead, reset to the fork branch directly:
```bash
git checkout -B deploy-private fork/deploy-private
```
This replaces your local branch with the fork's `deploy-private` cleanly. No conflicts possible.
The fork already carries all patches (CF Access, gateway mode, close-loop prompt, etc.).

If `git remote -v` does not show a `fork` remote, add it first:
```bash
git remote add fork git@github.com:Shaunwei/multica.git
git fetch fork
```

---

## Step 3 — Build CLI with Version Stamp

Detect target version from the latest tag or fork commit:

```bash
cd "$MULTICA_REPO"
# Get target version (from latest tag on fork, or from package.json)
TARGET_VERSION=$(python3 -c "import json; print('v' + json.load(open('apps/web/package.json'))['version'])")
echo "Building $TARGET_VERSION"

COMMIT=$(git rev-parse --short HEAD)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)

cd server
go build \
  -ldflags "-X 'main.version=$TARGET_VERSION' -X 'main.commit=$COMMIT' -X 'main.date=$DATE'" \
  -o bin/multica ./cmd/multica/

# Verify it shows the right version before installing
./bin/multica version   # must show TARGET_VERSION, not "dev"
```

---

## Step 4 — Install CLI Binary

**Critical:** Remove first, then copy. Overwriting in-place causes macOS to SIGKILL the new binary (exit 137).

**Also critical: refresh every `multica` on PATH, not just `/opt/homebrew/bin/multica`.** Some hosts have a second copy (commonly `~/bin/multica`) earlier in PATH. If you only replace one, `multica version` resolves the stale shadow and verification lies.

```bash
# Apply rm + cp + codesign to every multica binary earlier-or-equal in PATH,
# so the first-resolved multica is always the new one.
NEW_BIN="$MULTICA_REPO/server/bin/multica"
for dest in $(which -a multica); do
  rm "$dest"
  cp "$NEW_BIN" "$dest"
  codesign --sign - --force "$dest"
done
# Also ensure /opt/homebrew/bin/multica exists even if it wasn't on PATH before.
[ -e /opt/homebrew/bin/multica ] || { cp "$NEW_BIN" /opt/homebrew/bin/multica && codesign --sign - --force /opt/homebrew/bin/multica; }

multica version   # confirm correct version via PATH resolution
```

---

## Step 5 — Restart Daemon

LaunchAgent auto-restarts the daemon after kill. The new binary is picked up automatically.

```bash
kill $(pgrep -f "multica daemon start") 2>/dev/null
sleep 6
grep "starting daemon" ~/.multica/daemon-launchd.log | tail -2
# expect: version=TARGET_VERSION (NOT "dev")
```

**Expect transient bind-error noise.** While the old daemon's socket releases, LaunchAgent's retry loop may log a few:

```
Error: another daemon is already running on 127.0.0.1:19514: listen tcp 127.0.0.1:19514: bind: address already in use
```

These clear within a few seconds and are benign. Only fail if no `starting daemon` line with `version=TARGET_VERSION` appears within ~10 seconds of the kill.

---

## Step 6 — Verify

```bash
# CLI shows right version
multica version

# Daemon shows right version in log
grep "starting daemon" ~/.multica/daemon-launchd.log | tail -1

# Runtimes are online (may take 30s after restart)
multica runtime list
```

---

## Step 7 — Confirm Done

Report back with:
- Previous version → New version
- Daemon log line showing new version
- Runtime list showing runtimes online

---

## Repo Layout (reference)

- **Branch**: `deploy-private` (name is historical, carries all patched versions)
- **Remote name**: `fork` — run `git remote -v` to confirm the URL
- **Monorepo root**: under `~/ai/` — typically `~/ai/multica` or `~/ai/multica-private` (use the Prerequisite step to detect)
- **CLI source**: `server/cmd/multica/`
- **Daemon log**: `~/.multica/daemon-launchd.log`
- **LaunchAgent**: `com.multica.daemon`

## What NOT to Do

- Don't run `make migrate-up` — migrations only run on Air (the DB host)
- Don't build or restart the server binary — not present on worker nodes
- Don't build or restart the web UI — not present on worker nodes  
- Don't `git push origin main` — upstream is read-only
- Don't overwrite the CLI binary in place — always `rm` first to clear macOS exec cache
- Don't skip version stamp in `go build` — daemon log will show `dev` without `-ldflags`
- Don't refresh only `/opt/homebrew/bin/multica` — if another `multica` is earlier on PATH, verification lies. Refresh every entry returned by `which -a multica`.
