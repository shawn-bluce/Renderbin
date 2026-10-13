import { ensureApiKey, getSettings } from '$lib/api/settings';
import { listUsers } from '$lib/api/admin';

export async function load({ fetch, parent }) {
	const [{ user }, settings] = await Promise.all([parent(), getSettings(fetch)]);

	// Both of these are best-effort and independent, so they run side by side.
	//
	// The API key: MCP already enabled → surface it immediately (created lazily
	// on first visit, reused afterwards).
	//
	// The account list and the backup controls are super-admin-only sections of
	// this page. The server is the authority on that (every /api/admin handler
	// checks it itself), so a failure here just means "don't render the section"
	// rather than an error the page has to explain.
	const [apiKey, users] = await Promise.all([
		settings.mcp_enabled ? ensureApiKey(fetch).catch(() => null) : null,
		user.is_admin ? listUsers(fetch).catch(() => null) : null
	]);

	return { settings, apiKey, users };
}
