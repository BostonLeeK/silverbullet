# SilverBullet Bridge for Obsidian

Mirror a **sub-folder** of your Obsidian vault with a self-hosted
[SilverBullet](https://silverbullet.md) server. Everything outside that
sub-folder stays untouched, so existing Obsidian sync (Obsidian Sync, iCloud,
Syncthing, Git, etc.) keeps working for the rest of your vault.

## How it works

- You pick a folder inside your vault (default `silverbullet/`).
- On each sync the plugin:
  1. Fetches the server file list via `GET /.fs/` with `X-Sync-Mode: true`.
  2. Compares timestamps against local files in the sub-folder.
  3. Uploads / downloads / deletes as needed (last-write-wins).
- Conflicts (both sides changed since the last sync) are resolved in favor of
  the newest timestamp; the loser is kept as `<file>.conflict.<ts>.md`.

## Install

```bash
cd integrations/obsidian
npm install
npm run build
```

Copy `manifest.json` and the built `main.js` into a
`<vault>/.obsidian/plugins/silverbullet-bridge/` folder, then enable the plugin
from Obsidian's **Community plugins** list.

## Auth

- **Email + password** — click *Sign in…* in the settings tab.
- **Bearer token** — paste `SB_AUTH_TOKEN` value; recommended for mobile and
  for CI-style access.

## Hiding `Library/` and `Repositories/` in Obsidian

The synced folder contains SilverBullet’s standard tree (`Library/`,
`Repositories/`, etc.). Obsidian’s file explorer always shows real files; the
bridge does not hide them. To collapse clutter, add patterns under **Settings →
Files & links → Excluded files** (stored in `.obsidian/app.json` as
`userIgnoreFilters`), for example:

- `silverbullet/Library`
- `silverbullet/Repositories`

Adjust the prefix if your vault sub-folder name is not `silverbullet`.

## Known limitations

- Simple sync, not a CRDT. Simultaneous edits become a conflict file.
- Deletion detection relies on a local "last seen" index stored in plugin data.
  Clearing plugin data will cause missing files to be re-uploaded on the next
  sync instead of staying deleted.
