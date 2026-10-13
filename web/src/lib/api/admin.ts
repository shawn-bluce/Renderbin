import { api, apiVoid } from './http';

/**
 * Account management, super admin only — every endpoint here 403s for anyone
 * else. Note what is deliberately absent: there is no way to read another
 * account's files, so the counts are the most this surface exposes of them.
 */
export interface AdminUser {
	id: number;
	username: string;
	nickname: string;
	is_super_admin: boolean;
	disabled: boolean;
	created_at: string;
	file_count: number;
	trashed_count: number;
	/** Bytes stored by this account, trashed files included — the same figure
	 *  the upload check enforces against quota_bytes. */
	used_bytes: number;
	quota_bytes: number;
}

/** The one and only time an admin-created account's password is readable. */
export interface CreatedUser {
	id: number;
	username: string;
	nickname: string;
	password: string;
}

/** `fetch` is a parameter so SvelteKit `load` functions can pass their own. */
export function listUsers(f: typeof fetch = fetch): Promise<AdminUser[]> {
	return api('/api/admin/users', { fetch: f });
}

/**
 * Suspend or restore an account. Suspending blocks sign-in, invalidates the
 * sessions and MCP key it already has, and makes every link to its files 404.
 * Returns nothing: the caller flips its own row after a successful call, since
 * the server's row alone couldn't carry the file counts the list shows.
 */
export function setUserDisabled(id: number, disabled: boolean): Promise<void> {
	return apiVoid(`/api/admin/users/${id}/status`, { method: 'PATCH', json: { disabled } });
}

/** Set an account's password without knowing the old one, and sign it out everywhere. */
export function resetUserPassword(id: number, newPassword: string): Promise<void> {
	return apiVoid(`/api/admin/users/${id}/password`, {
		method: 'POST',
		json: { new_password: newPassword }
	});
}

/**
 * Create an account. The server generates the password and returns it once —
 * nothing stores it in plaintext, so the caller has to show it before the
 * response is discarded.
 */
export function createUser(username: string, nickname: string): Promise<CreatedUser> {
	return api('/api/admin/users', { method: 'POST', json: { username, nickname } });
}

/**
 * Delete an account and hard-delete every file it owns, in one transaction.
 * Irreversible and not routed through the trash — suspending is the reversible
 * option. The server refuses the super admin and the caller's own account.
 */
export function deleteUser(id: number): Promise<{ deleted_files: number }> {
	return api(`/api/admin/users/${id}`, { method: 'DELETE' });
}

/**
 * Change how much an account may store. Lowering it below current usage blocks
 * further uploads and leaves stored files alone.
 */
export function setUserQuota(id: number, quotaBytes: number): Promise<void> {
	return apiVoid(`/api/admin/users/${id}/quota`, {
		method: 'PATCH',
		json: { quota_bytes: quotaBytes }
	});
}
