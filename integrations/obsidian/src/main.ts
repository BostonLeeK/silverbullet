import {
    App,
    Notice,
    Plugin,
    PluginSettingTab,
    Setting,
    TFile,
    TFolder,
    normalizePath,
} from "obsidian";
import { ForbiddenError, NotFoundError, SbClient } from "./sbclient";

/**
 * SilverBullet Bridge for Obsidian
 *
 * Design goal: **do not interfere with existing Obsidian sync**. The plugin
 * only reads and writes under a user-configurable vault path (default
 * `silverbullet/`). Leave the path empty to mirror the vault root; then
 * `.obsidian/` and `.trash/` are ignored. Files outside the chosen path are
 * never touched.
 * Users who already sync their vault via Obsidian Sync / iCloud / Git can keep
 * doing so for everything else — the SilverBullet mirror is an addition, not a
 * replacement.
 *
 * Sync strategy is deliberately simple (last-write-wins by `lastModified`):
 *   - On every sync we fetch the remote file list and compare per-file
 *     timestamps against local counterparts.
 *   - If remote is newer, download; if local is newer, upload.
 *   - Deletions are detected via a local "known files" set persisted in
 *     settings — a file that was known and now missing on one side is deleted
 *     on the other.
 *
 * This is not a CRDT. Conflict = newest wins; the loser is saved as
 * `<file>.conflict.<ts>.md` alongside.
 */

interface BridgeSettings {
    serverUrl: string;
    email: string;
    cookie: string;
    bearerToken: string;
    folder: string;
    knownFiles: Record<string, number>; // path -> lastModified seen at last sync
    lastSync: number;
    autoSync: boolean;
    autoSyncDebounceMs: number;
}

function isShippedSbPath(p: string): boolean {
    return p.startsWith("Library/") || p.startsWith("Repositories/");
}

const DEFAULT_SETTINGS: BridgeSettings = {
    serverUrl: "",
    email: "",
    cookie: "",
    bearerToken: "",
    folder: "silverbullet",
    knownFiles: {},
    lastSync: 0,
    autoSync: true,
    autoSyncDebounceMs: 2500,
};

export default class SilverBulletBridgePlugin extends Plugin {
    settings!: BridgeSettings;
    private client!: SbClient;
    private syncing = false;
    loginPassword = "";
    private debounceTimer: number | null = null;

    async onload(): Promise<void> {
        await this.loadSettings();
        this.rebuildClient();

        this.register(() => {
            if (this.debounceTimer) {
                clearTimeout(this.debounceTimer);
                this.debounceTimer = null;
            }
        });

        const onMirrorPath = (path: string) => {
            if (!this.isUnderMirrorRoot(path)) return;
            this.scheduleDebouncedSync();
        };
        this.registerEvent(
            this.app.vault.on("modify", (f) => {
                if (f instanceof TFile) onMirrorPath(f.path);
            }),
        );
        this.registerEvent(
            this.app.vault.on("create", (f) => {
                if (f instanceof TFile) onMirrorPath(f.path);
            }),
        );
        this.registerEvent(this.app.vault.on("delete", (f) => onMirrorPath(f.path)));
        this.registerEvent(
            this.app.vault.on("rename", (f, oldPath) => {
                onMirrorPath(f.path);
                onMirrorPath(oldPath);
            }),
        );

        this.addRibbonIcon("refresh-cw", "SilverBullet: sync now", () => this.syncNow());
        this.addCommand({
            id: "silverbullet-sync",
            name: "Sync with SilverBullet",
            callback: () => this.syncNow(),
        });
        this.addCommand({
            id: "silverbullet-login",
            name: "Sign in to SilverBullet",
            callback: () => this.login(),
        });
        this.addSettingTab(new BridgeSettingTab(this.app, this));
    }

    onunload(): void {}

    private getMirrorRoot(): string {
        return normalizePath((this.settings.folder ?? "").trim());
    }

    private isObsidianInternalPath(p: string): boolean {
        return (
            p.startsWith(".obsidian/") ||
            p === ".obsidian" ||
            p.startsWith(".trash/")
        );
    }

    private isUnderMirrorRoot(vaultPath: string): boolean {
        const root = this.getMirrorRoot();
        const p = normalizePath(vaultPath);
        if (!root) {
            return !this.isObsidianInternalPath(p);
        }
        if (p === root) return true;
        return p.startsWith(root + "/");
    }

    private scheduleDebouncedSync(): void {
        if (!this.settings.autoSync) return;
        if (!this.settings.serverUrl) return;
        if (this.syncing) return;
        if (this.debounceTimer) clearTimeout(this.debounceTimer);
        const ms = Math.max(
            400,
            Math.min(120_000, this.settings.autoSyncDebounceMs || 2500),
        );
        this.debounceTimer = window.setTimeout(() => {
            this.debounceTimer = null;
            void this.syncNow({ quiet: true });
        }, ms);
    }

    rebuildClient(): void {
        this.client = new SbClient(
            this.settings.serverUrl,
            () => this.settings.cookie || undefined,
            async (c) => {
                this.settings.cookie = c;
                await this.saveSettings();
            },
            this.settings.bearerToken || undefined,
        );
    }

    async loadSettings(): Promise<void> {
        this.settings = Object.assign({}, DEFAULT_SETTINGS, await this.loadData());
    }

    async saveSettings(): Promise<void> {
        await this.saveData(this.settings);
    }

    async login(): Promise<void> {
        if (!this.settings.serverUrl) {
            new Notice("Configure server URL in settings first.");
            return;
        }
        if (!this.settings.email.trim()) {
            new Notice("Enter your email in settings.");
            return;
        }
        const trimmed = this.loginPassword.trim();
        const password =
            trimmed ||
            (await prompt(this.app, "SilverBullet password"))?.trim() ||
            "";
        if (!password) {
            new Notice(
                "Enter your password in the Password field (below Email), then tap Sign in.",
                8000,
            );
            return;
        }
        const busy = new Notice("SilverBullet: signing in…", 0);
        try {
            await this.client.login(this.settings.email.trim(), password);
            await this.saveSettings();
            this.loginPassword = "";
            busy.hide();
            new Notice("Logged in to SilverBullet.");
        } catch (e) {
            busy.hide();
            new Notice(`Login failed: ${(e as Error).message}`, 10000);
        }
    }

    private vaultPath(remotePath: string): string {
        const root = this.getMirrorRoot();
        const rel = normalizePath(remotePath);
        if (!root) return rel;
        return normalizePath(`${root}/${rel}`);
    }

    async syncNow(opts?: { quiet?: boolean }): Promise<void> {
        if (this.syncing) return;
        this.syncing = true;
        const notice = opts?.quiet ? null : new Notice("SilverBullet: syncing…", 0);
        try {
            await this.ensureFolder(this.getMirrorRoot());
            for (const k of Object.keys(this.settings.knownFiles)) {
                if (isShippedSbPath(k)) delete this.settings.knownFiles[k];
            }
            const remote = await this.client.listFiles();
            const remoteMap = new Map(remote.map((r) => [r.name, r]));
            const locals = await this.listLocalFiles();
            const localMap = new Map(locals.map((l) => [l.relPath, l]));

            const keys = new Set<string>([...remoteMap.keys(), ...localMap.keys()]);
            let uploaded = 0,
                downloaded = 0,
                deleted = 0,
                conflicts = 0,
                skipped = 0;

            for (const path of keys) {
                const r = remoteMap.get(path);
                const l = localMap.get(path);
                const known = this.settings.knownFiles[path];

                if (r && l) {
                    // Both exist — newer wins.
                    if (r.lastModified > l.mtime + 500) {
                        await this.downloadFile(path, r.lastModified);
                        downloaded++;
                    } else if (l.mtime > r.lastModified + 500) {
                        const u = await this.uploadFile(path, r.lastModified);
                        if (u === "uploaded") uploaded++;
                        else if (u === "redownloaded") downloaded++;
                        else if (u === "skipped") skipped++;
                    }
                } else if (r && !l) {
                    if (known !== undefined) {
                        if (isShippedSbPath(path)) {
                            delete this.settings.knownFiles[path];
                            skipped++;
                        } else {
                            try {
                                await this.client.deleteFile(path);
                                deleted++;
                            } catch (e) {
                                if (!(e instanceof ForbiddenError)) throw e;
                            }
                            delete this.settings.knownFiles[path];
                        }
                    } else {
                        await this.downloadFile(path, r.lastModified);
                        downloaded++;
                    }
                } else if (l && !r) {
                    if (known !== undefined) {
                        if (isShippedSbPath(path)) {
                            delete this.settings.knownFiles[path];
                            skipped++;
                        } else {
                            const f = this.app.vault.getAbstractFileByPath(this.vaultPath(path));
                            if (f instanceof TFile) await this.app.vault.delete(f);
                            delete this.settings.knownFiles[path];
                            deleted++;
                        }
                    } else {
                        const u = await this.uploadFile(path);
                        if (u === "uploaded") uploaded++;
                        else if (u === "redownloaded") downloaded++;
                        else if (u === "skipped") skipped++;
                    }
                }
            }
            this.settings.lastSync = Date.now();
            await this.saveSettings();
            let summary =
                `SilverBullet: ↓${downloaded} ↑${uploaded} ✖${deleted}` +
                (conflicts ? ` conflicts:${conflicts}` : "");
            if (skipped > 0) summary += ` ⊘${skipped}`;
            if (notice) {
                notice.setMessage(summary);
                setTimeout(() => notice.hide(), 4000);
            } else if (uploaded + downloaded + deleted > 0) {
                new Notice(summary, 3500);
            }
        } catch (e) {
            if (notice) notice.hide();
            new Notice(`Sync failed: ${(e as Error).message}`, 8000);
        } finally {
            this.syncing = false;
        }
    }

    private async listLocalFiles(): Promise<{ relPath: string; mtime: number; size: number }[]> {
        const out: { relPath: string; mtime: number; size: number }[] = [];
        const root = this.getMirrorRoot();
        if (!root) {
            for (const f of this.app.vault.getFiles()) {
                if (this.isObsidianInternalPath(f.path)) continue;
                if (isShippedSbPath(f.path)) continue;
                out.push({
                    relPath: f.path,
                    mtime: f.stat.mtime,
                    size: f.stat.size,
                });
            }
            return out;
        }
        const prefix = root + "/";
        for (const f of this.app.vault.getFiles()) {
            if (!f.path.startsWith(prefix)) continue;
            const relPath = f.path.slice(prefix.length);
            if (isShippedSbPath(relPath)) continue;
            out.push({
                relPath,
                mtime: f.stat.mtime,
                size: f.stat.size,
            });
        }
        return out;
    }

    private async ensureFolder(folder: string): Promise<void> {
        const p = normalizePath(folder);
        if (!p) return;
        const existing = this.app.vault.getAbstractFileByPath(p);
        if (existing instanceof TFolder) return;
        if (!existing) await this.app.vault.createFolder(p).catch(() => {});
    }

    private async downloadFile(path: string, remoteMtime: number): Promise<void> {
        try {
            const { data } = await this.client.readFile(path);
            const vaultPath = this.vaultPath(path);
            await this.ensureParentFolder(vaultPath);
            const existing = this.app.vault.getAbstractFileByPath(vaultPath);
            if (existing instanceof TFile) {
                // Conflict guard: if the local file was modified *after* the last known
                // remote mtime we already downloaded, save a conflict copy.
                const known = this.settings.knownFiles[path] ?? 0;
                if (existing.stat.mtime > known + 500) {
                    const conflictPath = `${vaultPath}.conflict.${existing.stat.mtime}.md`;
                    const localData = await this.app.vault.readBinary(existing);
                    await this.app.vault.createBinary(conflictPath, localData);
                }
                await this.app.vault.modifyBinary(existing, data);
            } else {
                await this.app.vault.createBinary(vaultPath, data);
            }
            this.settings.knownFiles[path] = remoteMtime;
        } catch (e) {
            if (e instanceof NotFoundError) return;
            throw e;
        }
    }

    private async uploadFile(
        path: string,
        serverMtime?: number,
    ): Promise<"uploaded" | "redownloaded" | "skipped"> {
        if (isShippedSbPath(path)) {
            if (serverMtime !== undefined) {
                await this.downloadFile(path, serverMtime);
                return "redownloaded";
            }
            return "skipped";
        }
        const vaultPath = this.vaultPath(path);
        const f = this.app.vault.getAbstractFileByPath(vaultPath);
        if (!(f instanceof TFile)) return "skipped";
        const data = await this.app.vault.readBinary(f);
        try {
            const meta = await this.client.writeFile(path, data);
            this.settings.knownFiles[path] = meta.lastModified;
            return "uploaded";
        } catch (e) {
            if (e instanceof ForbiddenError) {
                if (serverMtime !== undefined) {
                    await this.downloadFile(path, serverMtime);
                    new Notice(
                        `Read-only on server, restored from server: ${path}`,
                        6000,
                    );
                    return "redownloaded";
                }
                new Notice(`Skipped ${path}: no write permission on server.`);
                return "skipped";
            }
            throw e;
        }
    }

    private async ensureParentFolder(vaultPath: string): Promise<void> {
        const idx = vaultPath.lastIndexOf("/");
        if (idx < 0) return;
        const parent = vaultPath.slice(0, idx);
        await this.ensureFolder(parent);
    }

    async updateServer(
        serverUrl: string,
        email: string,
        bearerToken: string,
        folder: string,
    ): Promise<void> {
        this.settings.serverUrl = serverUrl.replace(/\/+$/, "");
        this.settings.email = email;
        this.settings.bearerToken = bearerToken;
        this.settings.folder = normalizePath(folder.trim());
        this.settings.cookie = "";
        this.settings.knownFiles = {};
        await this.saveSettings();
        this.rebuildClient();
    }
}

async function prompt(_app: App, title: string): Promise<string | null> {
    // Obsidian desktop exposes window.prompt via Electron; on mobile the user
    // should configure a bearer token instead (see settings).
    try {
        return window.prompt(title) ?? null;
    } catch {
        return null;
    }
}

class BridgeSettingTab extends PluginSettingTab {
    constructor(app: App, private plugin: SilverBulletBridgePlugin) {
        super(app, plugin);
    }

    display(): void {
        const { containerEl } = this;
        containerEl.empty();
        containerEl.createEl("h2", { text: "SilverBullet Bridge" });
        containerEl.createEl("p", {
            text:
                "Files under the path below mirror your SilverBullet space. Leave the path empty to use the vault root " +
                "(`.obsidian` and `.trash` are skipped). Anything outside the chosen path is left alone.",
        });

        const s = this.plugin.settings;

        new Setting(containerEl)
            .setName("Server URL")
            .setDesc("Base URL, e.g. https://notes.example.com")
            .addText((t) =>
                t.setValue(s.serverUrl).onChange(async (v) => {
                    s.serverUrl = v.trim();
                    await this.plugin.saveSettings();
                    this.plugin.rebuildClient();
                }),
            );

        new Setting(containerEl).setName("Email").addText((t) =>
            t.setValue(s.email).onChange(async (v) => {
                s.email = v.trim();
                await this.plugin.saveSettings();
            }),
        );

        new Setting(containerEl)
            .setName("Password")
            .setDesc(
                "Used only for Sign in (not saved to disk). Leave empty if you use a bearer token below.",
            )
            .addText((t) => {
                t.inputEl.type = "password";
                t.setPlaceholder("Your SilverBullet password");
                t.setValue(this.plugin.loginPassword);
                t.onChange((v) => {
                    this.plugin.loginPassword = v;
                });
            });

        new Setting(containerEl)
            .setName("Bearer token (optional)")
            .setDesc("If the server is configured with SB_AUTH_TOKEN you can use that instead of email/password.")
            .addText((t) =>
                t.setValue(s.bearerToken).onChange(async (v) => {
                    s.bearerToken = v.trim();
                    await this.plugin.saveSettings();
                    this.plugin.rebuildClient();
                }),
            );

        new Setting(containerEl)
            .setName("Vault path")
            .setDesc(
                "Only files under this path sync. Empty = vault root (skips .obsidian and .trash). Example: silverbullet",
            )
            .addText((t) =>
                t
                    .setPlaceholder("empty = vault root, or e.g. silverbullet")
                    .setValue(s.folder)
                    .onChange(async (v) => {
                        s.folder = normalizePath(v.trim());
                        await this.plugin.saveSettings();
                    }),
            );

        new Setting(containerEl)
            .setName("Auto-sync on vault changes")
            .setDesc(
                "After you stop editing (debounce), sync runs automatically. The Sync button still runs an immediate full sync.",
            )
            .addToggle((t) =>
                t.setValue(s.autoSync).onChange(async (v) => {
                    s.autoSync = v;
                    await this.plugin.saveSettings();
                }),
            );

        new Setting(containerEl)
            .setName("Auto-sync debounce (ms)")
            .setDesc("Wait this long after the last file change before syncing (400–120000).")
            .addText((t) =>
                t.setPlaceholder("2500").setValue(String(s.autoSyncDebounceMs)).onChange(async (v) => {
                    const n = parseInt(v.trim(), 10);
                    if (!Number.isFinite(n)) return;
                    s.autoSyncDebounceMs = n;
                    await this.plugin.saveSettings();
                }),
            );

        new Setting(containerEl).setName("Sign in").addButton((b) =>
            b.setButtonText("Sign in…").onClick(async () => {
                await this.plugin.login();
                this.display();
            }),
        );

        new Setting(containerEl).setName("Sync now").addButton((b) =>
            b.setButtonText("Sync").onClick(() => void this.plugin.syncNow()),
        );

        if (s.lastSync) {
            containerEl.createEl("p", {
                text: `Last sync: ${new Date(s.lastSync).toLocaleString()}`,
            });
        }
    }
}
