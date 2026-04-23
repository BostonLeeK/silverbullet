// Minimal HTTP client for the SilverBullet /.fs API, tailored for Obsidian's
// requestUrl (desktop) + fetch (mobile) environment.
import { requestUrl } from "obsidian";

export interface FileMeta {
    name: string;
    created: number;
    lastModified: number;
    contentType: string;
    size: number;
    perm: string;
}

export class SbClient {
    constructor(
        private baseUrl: string,
        private getCookie: () => string | undefined,
        private onCookie: (cookie: string) => void,
        private bearerToken?: string,
    ) {
        this.baseUrl = baseUrl.replace(/\/+$/, "");
    }

    private headers(extra?: Record<string, string>): Record<string, string> {
        const h: Record<string, string> = { ...(extra ?? {}) };
        if (this.bearerToken) h["Authorization"] = `Bearer ${this.bearerToken}`;
        const cookie = this.getCookie();
        if (cookie) h["Cookie"] = cookie;
        return h;
    }

    private async req(opts: {
        method: string;
        path: string;
        body?: string | ArrayBuffer;
        headers?: Record<string, string>;
        throwOn404?: boolean;
    }): Promise<{
        status: number;
        headers: Record<string, string>;
        arrayBuffer: ArrayBuffer;
        text: string;
    }> {
        const res = await requestUrl({
            url: `${this.baseUrl}${opts.path}`,
            method: opts.method,
            headers: this.headers(opts.headers),
            body: opts.body as any,
            throw: false,
        });
        const setCookie = res.headers["set-cookie"] ?? res.headers["Set-Cookie"];
        if (setCookie) {
            const cookie = (Array.isArray(setCookie) ? setCookie.join(",") : setCookie)
                .split(/,(?=[^ ]+=)/g)
                .map((c) => c.split(";")[0].trim())
                .join("; ");
            this.onCookie(cookie);
        }
        return {
            status: res.status,
            headers: res.headers as Record<string, string>,
            arrayBuffer: res.arrayBuffer,
            text: res.text,
        };
    }

    async login(email: string, password: string): Promise<void> {
        const body = new URLSearchParams({ email, password, rememberMe: "true" }).toString();
        const res = await this.req({
            method: "POST",
            path: "/.auth",
            headers: { "Content-Type": "application/x-www-form-urlencoded" },
            body,
        });
        if (res.status < 200 || res.status >= 300) {
            throw new Error(`Login failed (${res.status}): ${res.text}`);
        }
        let j: { status?: string; error?: string };
        try {
            j = JSON.parse(res.text);
        } catch {
            return;
        }
        if (j.status === "error") {
            throw new Error(j.error ?? "login failed");
        }
    }

    async ping(): Promise<void> {
        const res = await this.req({ method: "GET", path: "/.ping" });
        if (res.status !== 200) throw new Error(`Ping failed: ${res.status}`);
    }

    async listFiles(): Promise<FileMeta[]> {
        const res = await this.req({
            method: "GET",
            path: "/.fs/",
            headers: {
                "X-Sync-Mode": "true",
                "X-Sync-Omit-Shipped": "1",
            },
        });
        if (res.status !== 200) throw new Error(`List failed: ${res.status}`);
        return JSON.parse(res.text) as FileMeta[];
    }

    async readFile(path: string): Promise<{ data: ArrayBuffer; meta: FileMeta }> {
        const res = await this.req({ method: "GET", path: `/.fs/${encodePath(path)}` });
        if (res.status === 404) throw new NotFoundError(path);
        if (res.status !== 200) throw new Error(`Read ${path} failed: ${res.status}`);
        return { data: res.arrayBuffer, meta: metaFromHeaders(path, res.headers) };
    }

    async writeFile(path: string, data: ArrayBuffer, contentType?: string): Promise<FileMeta> {
        const res = await this.req({
            method: "PUT",
            path: `/.fs/${encodePath(path)}`,
            headers: {
                "Content-Type": contentType ?? guessContentType(path),
                "X-Content-Length": String(data.byteLength),
                "X-Permission": "rw",
            },
            body: data,
        });
        if (res.status === 403 || res.status === 409) throw new ForbiddenError(path);
        if (res.status < 200 || res.status >= 300) throw new Error(`Write ${path} failed: ${res.status}`);
        return metaFromHeaders(path, res.headers);
    }

    async deleteFile(path: string): Promise<void> {
        const res = await this.req({ method: "DELETE", path: `/.fs/${encodePath(path)}` });
        if (res.status === 404) return;
        if (res.status === 403 || res.status === 409) throw new ForbiddenError(path);
        if (res.status < 200 || res.status >= 300) throw new Error(`Delete ${path} failed: ${res.status}`);
    }
}

export class NotFoundError extends Error {}
export class ForbiddenError extends Error {}

function encodePath(p: string): string {
    return p.split("/").map(encodeURIComponent).join("/");
}

function metaFromHeaders(name: string, h: Record<string, string>): FileMeta {
    const g = (k: string) => h[k] ?? h[k.toLowerCase()] ?? "";
    return {
        name,
        created: Number(g("X-Created") || 0),
        lastModified: Number(g("X-Last-Modified") || 0),
        contentType: g("X-Content-Type") || g("Content-Type") || "",
        size: Number(g("X-Content-Length") || 0),
        perm: g("X-Permission") || "ro",
    };
}

function guessContentType(path: string): string {
    if (path.endsWith(".md")) return "text/markdown";
    if (path.endsWith(".json")) return "application/json";
    return "application/octet-stream";
}
