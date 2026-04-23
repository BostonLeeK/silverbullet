// Thin HTTP client for the SilverBullet REST API (/.fs/*, /.auth, /.ping).
//
// The server accepts a shared bearer token via the `Authorization: Bearer ...`
// header when SB_AUTH_TOKEN is configured. For cookie-based JWT auth this
// client logs in via POST /.auth and keeps the returned cookie.

export interface FileMeta {
    name: string;
    created: number;
    lastModified: number;
    contentType: string;
    size: number;
    perm: string;
}

export class SilverBulletClient {
    private cookie: string | undefined;

    constructor(
        private baseUrl: string,
        private bearerToken?: string,
    ) {
        this.baseUrl = baseUrl.replace(/\/+$/, "");
    }

    private async request(
        path: string,
        init: RequestInit & { asBuffer?: boolean } = {},
    ): Promise<Response> {
        const headers = new Headers(init.headers);
        if (this.bearerToken && !headers.has("Authorization")) {
            headers.set("Authorization", `Bearer ${this.bearerToken}`);
        }
        if (this.cookie && !headers.has("Cookie")) {
            headers.set("Cookie", this.cookie);
        }
        const res = await fetch(`${this.baseUrl}${path}`, {
            ...init,
            headers,
            redirect: "manual",
        });
        return res;
    }

    async ping(): Promise<{ version: string; spacePath: string }> {
        const res = await this.request("/.ping");
        if (!res.ok) throw new Error(`Ping failed: ${res.status}`);
        return {
            version: res.headers.get("X-Server-Version") ?? "",
            spacePath: res.headers.get("X-Space-Path") ?? "",
        };
    }

    /** Login with email + password; stores the session cookie. */
    async login(email: string, password: string): Promise<void> {
        const body = new URLSearchParams({ email, password, rememberMe: "true" });
        const res = await this.request("/.auth", {
            method: "POST",
            headers: { "Content-Type": "application/x-www-form-urlencoded" },
            body: body.toString(),
        });
        const setCookie = res.headers.get("set-cookie");
        if (res.ok && setCookie) {
            // Keep just the name=value part of each cookie.
            this.cookie = setCookie
                .split(/,(?=[^ ]+=)/g)
                .map((c) => c.split(";")[0].trim())
                .join("; ");
            return;
        }
        let err = "Login failed";
        try {
            const j = (await res.json()) as { error?: string };
            if (j.error) err = j.error;
        } catch { /* ignore */ }
        throw new Error(err);
    }

    getCookie(): string | undefined {
        return this.cookie;
    }

    setCookie(c: string): void {
        this.cookie = c;
    }

    async listFiles(): Promise<FileMeta[]> {
        const res = await this.request("/.fs/", {
            headers: {
                "X-Sync-Mode": "true",
                "X-Sync-Omit-Shipped": "1",
            },
        });
        if (!res.ok) throw new Error(`List failed: ${res.status}`);
        return (await res.json()) as FileMeta[];
    }

    async getMeta(path: string): Promise<FileMeta | null> {
        const res = await this.request(`/.fs/${encodePath(path)}`, {
            headers: { "X-Get-Meta": "true" },
        });
        if (res.status === 404) return null;
        if (!res.ok) throw new Error(`Get meta failed: ${res.status}`);
        return metaFromHeaders(path, res.headers);
    }

    async readFile(path: string): Promise<{ data: Uint8Array; meta: FileMeta }> {
        const res = await this.request(`/.fs/${encodePath(path)}`);
        if (res.status === 404) throw new NotFoundError(path);
        if (!res.ok) throw new Error(`Read failed: ${res.status}`);
        const buf = new Uint8Array(await res.arrayBuffer());
        return { data: buf, meta: metaFromHeaders(path, res.headers) };
    }

    async writeFile(path: string, data: Uint8Array, contentType?: string): Promise<FileMeta> {
        const headers: Record<string, string> = {
            "Content-Type": contentType ?? guessContentType(path),
            "X-Content-Length": String(data.byteLength),
            "X-Permission": "rw",
        };
        const res = await this.request(`/.fs/${encodePath(path)}`, {
            method: "PUT",
            headers,
            body: data,
        });
        if (res.status === 403) throw new ForbiddenError(path);
        if (!res.ok) throw new Error(`Write failed: ${res.status}`);
        return metaFromHeaders(path, res.headers);
    }

    async deleteFile(path: string): Promise<void> {
        const res = await this.request(`/.fs/${encodePath(path)}`, { method: "DELETE" });
        if (res.status === 404) throw new NotFoundError(path);
        if (res.status === 403) throw new ForbiddenError(path);
        if (!res.ok) throw new Error(`Delete failed: ${res.status}`);
    }
}

export class NotFoundError extends Error {
    constructor(path: string) {
        super(`Not found: ${path}`);
    }
}

export class ForbiddenError extends Error {
    constructor(path: string) {
        super(`Forbidden: ${path}`);
    }
}

function encodePath(p: string): string {
    return p.split("/").map(encodeURIComponent).join("/");
}

function metaFromHeaders(name: string, h: Headers): FileMeta {
    return {
        name,
        created: Number(h.get("X-Created") ?? 0),
        lastModified: Number(h.get("X-Last-Modified") ?? 0),
        contentType: h.get("X-Content-Type") ?? h.get("Content-Type") ?? "",
        size: Number(h.get("X-Content-Length") ?? 0),
        perm: h.get("X-Permission") ?? "ro",
    };
}

function guessContentType(path: string): string {
    if (path.endsWith(".md")) return "text/markdown";
    if (path.endsWith(".json")) return "application/json";
    if (path.endsWith(".txt")) return "text/plain";
    return "application/octet-stream";
}
