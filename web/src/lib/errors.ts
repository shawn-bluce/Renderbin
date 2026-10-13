import { ApiError } from '$lib/api/http';
import { t } from '$lib/i18n/index.svelte';
import type { MessageKey } from '$lib/i18n/messages';

/**
 * The message to show for a failed call. The server's own text is shown only
 * when the server actually answered (an ApiError); anything else -- a dropped
 * connection, a thrown TypeError -- gets the translated fallback instead of a
 * raw "Failed to fetch".
 */
export function errText(
	err: unknown,
	fallback: MessageKey,
	params?: Record<string, string | number>
): string {
	return err instanceof ApiError && err.message ? err.message : t(fallback, params);
}
