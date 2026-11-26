<script lang="ts">
	// The comments, floating at the right edge of the document. Each note
	// sits level with the passage it is about (the framed document reports where
	// that is, and how far it has scrolled), and notes that would overlap are
	// stacked (see $lib/notes-layout.ts). A note whose passage is no longer in the
	// document cannot be placed, so it waits in a small list instead of being
	// lost. An AI explanation in progress is a note of its own.
	import Spinner from '$lib/components/Spinner.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { layoutNotes } from '$lib/notes-layout';
	import type { Measured } from '$lib/selection';
	import type { Comment } from '$lib/api/comments';
	import NoteCard from './NoteCard.svelte';

	export interface Thread {
		c: Comment;
		replies: Comment[];
	}

	/** An explanation being fetched (or that failed), anchored at where it was asked. */
	export interface Pending {
		key: number;
		quote: string;
		/** Distance of the selection from the top of the document. */
		top: number;
		error: string | null;
	}

	let {
		threads,
		pendings,
		anchors,
		timedOut,
		scrollY,
		onlocate,
		onreply,
		ondelete,
		ondismiss
	}: {
		threads: Thread[];
		pendings: Pending[];
		/** Where the framed document found each comment's passage. */
		anchors: Map<number, Measured>;
		/** The document never answered a measurement: everything is listed as unplaced. */
		timedOut: boolean;
		scrollY: number;
		onlocate: (quote: string) => void;
		onreply: (parent: Comment, body: string) => Promise<boolean>;
		ondelete: (c: Comment) => void;
		ondismiss: (key: number) => void;
	} = $props();

	let heights = $state<Record<string, number>>({});

	// Notes are placed against the framed document, which starts at the top of the
	// viewport; this region does too, but the offset is measured rather than assumed.
	let region = $state<HTMLElement>();
	let regionTop = $state(0);
	$effect(() => {
		if (!region) return;
		const el = region;
		const update = () => (regionTop = el.getBoundingClientRect().top);
		update();
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	});

	// A comment is placed, unplaced, or not yet measured (hidden for the moment
	// the document takes to answer, unless it never will).
	const placed = $derived(threads.filter((th) => anchors.get(th.c.id)?.found));
	const unplaced = $derived(
		threads.filter((th) => {
			const m = anchors.get(th.c.id);
			return m ? !m.found : timedOut;
		})
	);

	const tops = $derived(
		layoutNotes([
			...placed.map((th) => ({
				key: `c${th.c.id}`,
				anchor: anchors.get(th.c.id)!.top,
				height: heights[`c${th.c.id}`] ?? 0
			})),
			...pendings.map((p) => ({
				key: `p${p.key}`,
				anchor: p.top,
				height: heights[`p${p.key}`] ?? 0
			}))
		])
	);
	const shift = (key: string) => (tops.get(key) ?? 0) - scrollY - regionTop;
</script>

<!-- Floating over the document, like the "Ask AI" button: no panel, no
     background, nothing that narrows the document. Only the notes themselves
     take clicks. -->
<aside
	class="pointer-events-none fixed inset-y-0 right-0 z-30 w-72 max-w-[85vw] overflow-hidden"
	aria-label={t('comments.title')}
>
	<div bind:this={region} class="absolute inset-0">
		{#each placed as th (th.c.id)}
			<div
				class="pointer-events-auto absolute top-0 right-3 left-3 will-change-transform"
				style="transform: translateY({shift(`c${th.c.id}`)}px)"
				bind:clientHeight={heights[`c${th.c.id}`]}
			>
				<NoteCard c={th.c} replies={th.replies} anchored {onlocate} {onreply} {ondelete} />
			</div>
		{/each}

		{#each pendings as p (p.key)}
			<div
				class="pointer-events-auto absolute top-0 right-3 left-3 will-change-transform"
				style="transform: translateY({shift(`p${p.key}`)}px)"
				bind:clientHeight={heights[`p${p.key}`]}
			>
				<article class="note rounded-lg border border-line bg-surface p-2.5 text-sm">
					<div class="flex items-center gap-1.5 text-xs text-faint">
						<span class="chip chip-info px-1.5 py-0 font-mono text-[10px] font-semibold">
							{t('comments.ai')}
						</span>
						{#if !p.error}
							<Spinner />
							<span>{t('comments.explaining')}</span>
						{/if}
					</div>
					<p class="mt-1.5 border-l-2 border-accent pl-2 text-xs break-words text-muted">
						<span class="line-clamp-2">{p.quote}</span>
					</p>
					{#if p.error}
						<p class="mt-1.5 text-xs text-bad">{p.error}</p>
						<button
							type="button"
							class="btn btn-ghost btn-sm mt-1 -ml-1.5"
							onclick={() => ondismiss(p.key)}
						>
							{t('comments.dismiss')}
						</button>
					{/if}
				</article>
			</div>
		{/each}

		<!-- Notes whose passage is gone cannot be placed; they wait here, above
		     the buttons, instead of being lost. -->
		{#if unplaced.length}
			<div
				class="pointer-events-auto absolute right-3 bottom-[148px] left-3 flex max-h-[40%] flex-col gap-2 overflow-y-auto"
			>
				<p class="px-1 text-xs text-muted">{t('comments.unplaced')}</p>
				{#each unplaced as th (th.c.id)}
					<NoteCard
						c={th.c}
						replies={th.replies}
						anchored={false}
						{onlocate}
						{onreply}
						{ondelete}
					/>
				{/each}
			</div>
		{/if}
	</div>
</aside>
