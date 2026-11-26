<script lang="ts">
	// The small input that opens where text was selected. It is drawn by this
	// page, over the framed document, rather than inside it: what is typed stays
	// in the app's origin, and the document's own scripts never see it.
	import { tick } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import type { Rect } from '$lib/selection';

	let {
		rect,
		quote,
		maxRight,
		onsubmit,
		oncancel,
		ondraft
	}: {
		/** The selection's box in the viewport. */
		rect: Rect;
		quote: string;
		/** The right edge available to the popover (the framed document's width). */
		maxRight: number;
		/** Posts the comment; resolves true when it was stored. */
		onsubmit: (body: string) => Promise<boolean>;
		oncancel: () => void;
		/** Reports whether anything has been typed, so a click elsewhere can close an empty one. */
		ondraft: (hasText: boolean) => void;
	} = $props();

	const WIDTH = 288;
	const HEIGHT = 168; // a generous guess: only decides above or below the selection

	let body = $state('');
	let busy = $state(false);
	let box = $state<HTMLTextAreaElement>();

	const left = $derived(Math.max(8, Math.min(rect.x, maxRight - WIDTH - 8)));
	const top = $derived.by(() => {
		const below = rect.y + rect.h + 8;
		return below + HEIGHT > window.innerHeight - 8 ? Math.max(8, rect.y - HEIGHT - 8) : below;
	});

	$effect(() => {
		tick().then(() => box?.focus());
	});
	$effect(() => ondraft(body.trim() !== ''));

	async function post() {
		const text = body.trim();
		if (!text || busy) return;
		busy = true;
		if (!(await onsubmit(text))) busy = false;
	}
</script>

<form
	class="fixed z-50 flex flex-col gap-2 rounded-lg border border-line-strong bg-surface p-2.5 shadow-[0_6px_24px_rgba(0,0,0,0.22)]"
	style="left: {left}px; top: {top}px; width: {WIDTH}px"
	aria-label={t('comments.title')}
	onsubmit={(e) => {
		e.preventDefault();
		post();
	}}
>
	<p class="truncate border-l-2 border-accent pl-2 text-xs text-muted" title={quote}>{quote}</p>
	<textarea
		bind:this={box}
		bind:value={body}
		onkeydown={(e) => {
			if (e.key === 'Escape') {
				e.preventDefault();
				oncancel();
			} else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.isComposing) {
				e.preventDefault();
				post();
			}
		}}
		rows="3"
		placeholder={t('comments.placeholder')}
		aria-label={t('comments.placeholder')}
		class="input min-h-[3.5rem] resize-none py-2 text-sm [field-sizing:content]"></textarea>
	<div class="flex justify-end gap-1.5">
		<button type="button" class="btn btn-ghost btn-sm" onclick={oncancel}>
			{t('comments.cancel')}
		</button>
		<button type="submit" class="btn btn-primary btn-sm" disabled={busy || !body.trim()}>
			{t('comments.post')}
		</button>
	</div>
</form>
