import * as vscode from "vscode";
import { SilverBulletClient } from "./client";
import { SilverBulletFs } from "./fs-provider";

const SCHEME = "silverbullet";
const SECRET_KEY_PREFIX = "silverbullet.credentials."; // + authority

interface Connection {
    serverUrl: string;
    email?: string;
    cookie?: string;
    bearerToken?: string;
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
    const fs = new SilverBulletFs();
    context.subscriptions.push(
        vscode.workspace.registerFileSystemProvider(SCHEME, fs, {
            isCaseSensitive: true,
        }),
    );

    context.subscriptions.push(
        vscode.commands.registerCommand("silverbullet.connect", () => connect(context, fs)),
        vscode.commands.registerCommand("silverbullet.disconnect", () => disconnect(context, fs)),
        vscode.commands.registerCommand("silverbullet.openSpace", () => openSpace(context, fs)),
    );

    // Restore any saved connections from previous sessions.
    const saved = context.globalState.get<Record<string, Connection>>("connections", {});
    for (const [authority, conn] of Object.entries(saved)) {
        const client = new SilverBulletClient(conn.serverUrl, conn.bearerToken);
        const secret = await context.secrets.get(SECRET_KEY_PREFIX + authority);
        if (secret) client.setCookie(secret);
        fs.registerClient(authority, client);
    }
}

export function deactivate(): void {}

async function connect(context: vscode.ExtensionContext, fs: SilverBulletFs): Promise<void> {
    const serverUrl = await vscode.window.showInputBox({
        title: "SilverBullet server URL",
        placeHolder: "https://notes.example.com",
        value: vscode.workspace.getConfiguration("silverbullet").get<string>("serverUrl") ?? "",
        validateInput: (v) => (/^https?:\/\//.test(v) ? null : "Must start with http:// or https://"),
    });
    if (!serverUrl) return;

    const mode = await vscode.window.showQuickPick(
        [
            { label: "Email + password", value: "password" },
            { label: "Bearer token (SB_AUTH_TOKEN)", value: "token" },
        ],
        { title: "Authentication method" },
    );
    if (!mode) return;

    const authority = new URL(serverUrl).host;
    let client: SilverBulletClient;
    let conn: Connection = { serverUrl };

    if (mode.value === "password") {
        const email = await vscode.window.showInputBox({ title: "Email", ignoreFocusOut: true });
        if (!email) return;
        const password = await vscode.window.showInputBox({
            title: "Password",
            password: true,
            ignoreFocusOut: true,
        });
        if (!password) return;
        client = new SilverBulletClient(serverUrl);
        try {
            await client.login(email, password);
        } catch (e) {
            vscode.window.showErrorMessage(`Login failed: ${(e as Error).message}`);
            return;
        }
        conn.email = email;
        conn.cookie = client.getCookie();
        if (conn.cookie) {
            await context.secrets.store(SECRET_KEY_PREFIX + authority, conn.cookie);
        }
    } else {
        const token = await vscode.window.showInputBox({
            title: "Bearer token",
            password: true,
            ignoreFocusOut: true,
        });
        if (!token) return;
        client = new SilverBulletClient(serverUrl, token);
        conn.bearerToken = token;
        await context.secrets.store(SECRET_KEY_PREFIX + authority, token);
    }

    try {
        await client.ping();
    } catch (e) {
        vscode.window.showErrorMessage(`Cannot reach server: ${(e as Error).message}`);
        return;
    }

    fs.registerClient(authority, client);
    const saved = context.globalState.get<Record<string, Connection>>("connections", {});
    // Don't persist the raw cookie/token in globalState; keep those in SecretStorage.
    saved[authority] = { serverUrl: conn.serverUrl, email: conn.email };
    await context.globalState.update("connections", saved);

    const uri = vscode.Uri.parse(`${SCHEME}://${authority}/`);
    const open = await vscode.window.showInformationMessage(
        `Connected to ${authority}. Open as workspace folder?`,
        "Open",
        "Cancel",
    );
    if (open === "Open") {
        vscode.workspace.updateWorkspaceFolders(
            vscode.workspace.workspaceFolders?.length ?? 0,
            0,
            { uri, name: `SilverBullet (${authority})` },
        );
    }
}

async function disconnect(context: vscode.ExtensionContext, fs: SilverBulletFs): Promise<void> {
    const saved = context.globalState.get<Record<string, Connection>>("connections", {});
    const keys = Object.keys(saved);
    if (keys.length === 0) {
        vscode.window.showInformationMessage("No SilverBullet connections.");
        return;
    }
    const pick = await vscode.window.showQuickPick(keys, { title: "Disconnect from..." });
    if (!pick) return;
    fs.removeClient(pick);
    delete saved[pick];
    await context.globalState.update("connections", saved);
    await context.secrets.delete(SECRET_KEY_PREFIX + pick);
}

async function openSpace(context: vscode.ExtensionContext, _fs: SilverBulletFs): Promise<void> {
    const saved = context.globalState.get<Record<string, Connection>>("connections", {});
    const keys = Object.keys(saved);
    if (keys.length === 0) {
        await connect(context, _fs);
        return;
    }
    const pick = await vscode.window.showQuickPick(keys, { title: "Open SilverBullet space..." });
    if (!pick) return;
    const uri = vscode.Uri.parse(`${SCHEME}://${pick}/`);
    vscode.workspace.updateWorkspaceFolders(
        vscode.workspace.workspaceFolders?.length ?? 0,
        0,
        { uri, name: `SilverBullet (${pick})` },
    );
}
