# SilverBullet for VS Code / Cursor

Connect to a self-hosted [SilverBullet](https://silverbullet.md) server and
edit your notes directly from VS Code (or Cursor — same extension API).
Implemented as a virtual `FileSystemProvider`, so every file is fetched from
and saved back to the server over its REST `/.fs/*` API.

## Features

- Email + password or bearer-token authentication.
- Multiple simultaneous connections (one per server host).
- Read, write, rename, delete; folders are synthesized from file paths.
- Respects server-side folder ACLs — files you only have `reader` rights to are
  shown read-only; writes return `NoPermissions`.

## Commands

| Command | Description |
|---------|-------------|
| `SilverBullet: Connect to server` | Prompt for URL + credentials, verify connection. |
| `SilverBullet: Open space as workspace folder` | Mount a connected server as `silverbullet://host/`. |
| `SilverBullet: Disconnect` | Forget a server and its saved credentials. |

## Building

```bash
cd integrations/vscode
npm install
npm run compile
```

Then press `F5` in VS Code with this folder open to launch an Extension Host
for local development.
