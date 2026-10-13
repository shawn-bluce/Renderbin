// "Copy, then show a check mark for a moment." Every copy button in the app goes
// through copyText (see clipboard.ts for why); this adds the transient feedback.
import { copyText } from '$lib/clipboard';

/**
 * `key` tells apart several buttons sharing one feedback state (a row per file);
 * a lone button can leave it out. `copy` resolves false when the browser refused,
 * so the caller can put the text somewhere selectable instead.
 */
export function copyFeedback(ms = 1500) {
	let copied = $state<string | null>(null);
	let timer: ReturnType<typeof setTimeout> | undefined;
	return {
		is: (key = '') => copied === key,
		async copy(text: string, key = ''): Promise<boolean> {
			const ok = await copyText(text);
			if (ok) {
				copied = key;
				clearTimeout(timer);
				timer = setTimeout(() => (copied = null), ms);
			}
			return ok;
		}
	};
}
