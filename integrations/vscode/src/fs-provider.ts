import * as vscode from "vscode";
import {
    FileMeta,
    ForbiddenError,
    NotFoundError,
    SilverBulletClient,
} from "./client";

/**
 * Exposes a SilverBullet server as a virtual filesystem in VS Code.
 *
 * URIs look like:  silverbullet://<authority>/<relative/path>
 *
 * The authority is just an opaque key tying the URI to a configured server
 * connection (kept in the manager). We keep file metadata in an in-memory
 * cache that is refreshed lazily.
 */
export class SilverBulletFs implements vscode.FileSystemProvider {
    private readonly emitter = new vscode.EventEmitter<vscode.FileChangeEvent[]>();
    readonly onDidChangeFile = this.emitter.event;

    // authority -> client
    private readonly clients = new Map<string, SilverBulletClient>();
    // authority -> path -> meta
    private readonly metaCache = new Map<string, Map<string, FileMeta>>();

    registerClient(authority: string, client: SilverBulletClient): void {
        this.clients.set(authority, client);
        this.metaCache.set(authority, new Map());
    }

    removeClient(authority: string): void {
        this.clients.delete(authority);
        this.metaCache.delete(authority);
    }

    private getClient(uri: vscode.Uri): SilverBulletClient {
        const c = this.clients.get(uri.authority);
        if (!c) throw new Error(`No SilverBullet connection for ${uri.authority}`);
        return c;
    }

    private async getMetaMap(authority: string): Promise<Map<string, FileMeta>> {
        let map = this.metaCache.get(authority);
        if (!map) {
            map = new Map();
            this.metaCache.set(authority, map);
        }
        if (map.size === 0) {
            const client = this.clients.get(authority);
            if (client) {
                const files = await client.listFiles();
                for (const f of files) map.set(f.name, f);
            }
        }
        return map;
    }

    private toPath(uri: vscode.Uri): string {
        return uri.path.replace(/^\/+/, "");
    }

    watch(): vscode.Disposable {
        // Pull-based; nothing to watch. VS Code polls via stat/readFile.
        return { dispose: () => {} };
    }

    async stat(uri: vscode.Uri): Promise<vscode.FileStat> {
        const path = this.toPath(uri);
        if (path === "") {
            return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
        }
        const map = await this.getMetaMap(uri.authority);
        const meta = map.get(path);
        if (meta) return metaToStat(meta, vscode.FileType.File);
        // Might be a directory synthesized from children
        const prefix = path.endsWith("/") ? path : path + "/";
        for (const key of map.keys()) {
            if (key.startsWith(prefix)) {
                return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
            }
        }
        // Fall back to a live meta fetch (for freshly-created files)
        const live = await this.getClient(uri).getMeta(path).catch(() => null);
        if (live) {
            map.set(path, live);
            return metaToStat(live, vscode.FileType.File);
        }
        throw vscode.FileSystemError.FileNotFound(uri);
    }

    async readDirectory(uri: vscode.Uri): Promise<[string, vscode.FileType][]> {
        const path = this.toPath(uri);
        const prefix = path === "" ? "" : path + "/";
        const map = await this.getMetaMap(uri.authority);
        const dirs = new Set<string>();
        const files: [string, vscode.FileType][] = [];
        for (const key of map.keys()) {
            if (!key.startsWith(prefix)) continue;
            const rest = key.slice(prefix.length);
            const slash = rest.indexOf("/");
            if (slash === -1) {
                files.push([rest, vscode.FileType.File]);
            } else {
                dirs.add(rest.slice(0, slash));
            }
        }
        return [
            ...[...dirs].map((d) => [d, vscode.FileType.Directory] as [string, vscode.FileType]),
            ...files,
        ];
    }

    async readFile(uri: vscode.Uri): Promise<Uint8Array> {
        const path = this.toPath(uri);
        try {
            const { data, meta } = await this.getClient(uri).readFile(path);
            (await this.getMetaMap(uri.authority)).set(path, meta);
            return data;
        } catch (e) {
            if (e instanceof NotFoundError) throw vscode.FileSystemError.FileNotFound(uri);
            if (e instanceof ForbiddenError) throw vscode.FileSystemError.NoPermissions(uri);
            throw e;
        }
    }

    async writeFile(
        uri: vscode.Uri,
        content: Uint8Array,
        options: { create: boolean; overwrite: boolean },
    ): Promise<void> {
        const path = this.toPath(uri);
        const map = await this.getMetaMap(uri.authority);
        const existed = map.has(path);
        if (!options.create && !existed) throw vscode.FileSystemError.FileNotFound(uri);
        if (!options.overwrite && existed) throw vscode.FileSystemError.FileExists(uri);
        try {
            const meta = await this.getClient(uri).writeFile(path, content);
            map.set(path, meta);
            this.emitter.fire([
                {
                    type: existed ? vscode.FileChangeType.Changed : vscode.FileChangeType.Created,
                    uri,
                },
            ]);
        } catch (e) {
            if (e instanceof ForbiddenError) throw vscode.FileSystemError.NoPermissions(uri);
            throw e;
        }
    }

    async delete(uri: vscode.Uri): Promise<void> {
        const path = this.toPath(uri);
        try {
            await this.getClient(uri).deleteFile(path);
        } catch (e) {
            if (e instanceof NotFoundError) throw vscode.FileSystemError.FileNotFound(uri);
            if (e instanceof ForbiddenError) throw vscode.FileSystemError.NoPermissions(uri);
            throw e;
        }
        (await this.getMetaMap(uri.authority)).delete(path);
        this.emitter.fire([{ type: vscode.FileChangeType.Deleted, uri }]);
    }

    async rename(oldUri: vscode.Uri, newUri: vscode.Uri): Promise<void> {
        // SilverBullet has no rename primitive — emulate via read + write + delete.
        const data = await this.readFile(oldUri);
        await this.writeFile(newUri, data, { create: true, overwrite: true });
        await this.delete(oldUri);
    }

    createDirectory(): void {
        // Directories are implicit (derived from file paths). VS Code accepts a no-op.
    }
}

function metaToStat(meta: FileMeta, type: vscode.FileType): vscode.FileStat {
    const permissions = meta.perm === "ro" ? vscode.FilePermission.Readonly : undefined;
    return {
        type,
        ctime: meta.created,
        mtime: meta.lastModified,
        size: meta.size,
        permissions,
    };
}
