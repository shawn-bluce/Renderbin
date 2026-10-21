import { api, apiVoid } from './http';

export interface Settings {
	allow_registration: boolean;
	mcp_enabled: boolean;
	upload_default_public: boolean;
	qr_widget: boolean;
}

export function getSettings(f?: typeof fetch): Promise<Settings> {
	return api('/api/settings', { fetch: f });
}

/** Super-admin only; fields are optional so a single toggle can be sent alone. */
export function updateSettings(patch: Partial<Settings>): Promise<Settings> {
	return api('/api/settings', { method: 'PUT', json: patch });
}

export interface Usage {
	used_bytes: number;
	quota_bytes: number;
}

/**
 * The caller's storage use and limit. Its own endpoint rather than a field on
 * `/api/auth/me` because the layout guard hits that on every navigation and
 * this one runs an aggregate. Counts trashed files, matching what the server
 * enforces on upload, so the figure shown is the figure that will be applied.
 */
export function getUsage(f?: typeof fetch): Promise<Usage> {
	return api('/api/user/usage', { fetch: f });
}

export interface ProfilePatch {
	nickname?: string;
	current_password?: string;
	new_password?: string;
}

export function updateProfile(patch: ProfilePatch): Promise<void> {
	return apiVoid('/api/user', { method: 'PATCH', json: patch });
}

async function apiKeyRequest(path: string, f?: typeof fetch): Promise<string> {
	const body = await api<{ api_key: string }>(path, { method: 'POST', fetch: f });
	return body.api_key;
}

/** Returns the current user's MCP API key, creating one on first call. */
export function ensureApiKey(f?: typeof fetch): Promise<string> {
	return apiKeyRequest('/api/user/api-key', f);
}

/** Replaces the current user's MCP API key with a fresh one. */
export function resetApiKey(): Promise<string> {
	return apiKeyRequest('/api/user/api-key/reset');
}

export interface RestoreResult {
	users: number;
	files: number;
}

/**
 * Replaces all data with an uploaded backup. Super admin only.
 *
 * The body is the raw backup file (the `.tar` /api/backup produces, or a `.db`
 * from an older version) — there is exactly one field, so a multipart wrapper
 * would only add parsing on both ends. The server needs its size up front,
 * which a File body provides. A rejected upload changes
 * nothing, but a *successful* one replaces the sessions table too, so the caller
 * should expect its own session to be gone and reload.
 */
export function restoreBackup(file: File): Promise<RestoreResult> {
	return api('/api/backup/restore', {
		method: 'POST',
		body: file,
		type: 'application/octet-stream'
	});
}
