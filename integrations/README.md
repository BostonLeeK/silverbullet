# SilverBullet integrations

External clients that talk to a SilverBullet server via its REST API (`/.fs/*`,
`/.auth/*`, `/.ping`). They are independent projects with their own build
pipelines and do not affect the main server build.

| Folder | What it is |
|--------|------------|
| [`vscode/`](./vscode) | VS Code / Cursor extension. Registers a `silverbullet://` FileSystemProvider so your notes appear as a workspace folder you can edit natively. |
| [`obsidian/`](./obsidian) | Obsidian plugin that mirrors a vault sub-folder with a SilverBullet server. Leaves files outside that sub-folder alone so existing Obsidian sync keeps working. |

Helix has no extension API that lets a plugin implement a virtual
filesystem, so a Helix integration would need a companion CLI
(`sb-cli pull / push / watch`) talking to the same endpoints — not yet
implemented.
