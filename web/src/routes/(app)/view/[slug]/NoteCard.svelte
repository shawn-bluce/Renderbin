<script lang="ts">
	// One comment thread as a margin note: who wrote it and when, the passage it
	// is about, the text, and the replies under it. An AI explanation is the same
	// card with an "AI" mark; its text is Markdown from the model, so it goes
	// through the sanitizer, while everything people wrote is shown as text.
	import Icon from '$lib/components/Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { formatDeadline } from '$lib/dates';
	import { renderMarkdown } from '$lib/markdown';
	import type { Comment } from '$lib/api/comments';

	let {
		c,
		replies,
		anchored,
		onlocate,
		onreply,
		ondelete
	}: {
		c: Comment;
		replies: Comment[];
		/** The passage is in the document right now; a note whose passage is gone says so. */
		anchored: boolean;
		onlocate: (quote: string) => void;
		/** Posts a reply; resolves true when it was stored. */
		onreply: (parent: Comment, body: string) => Promise<boolean>;
		ondelete: (c: Comment) => void;
	} = $props();

	let replying = $state(false);
	let draft = $state('');
	let busy = $state(false);
	let box = $state<HTMLTextAreaElement>();

	async function reply() {
		const body = draft.trim();
		if (!body || busy) return;
		busy = true;
		if (await onreply(c, body)) {
			draft = '';
			replying = false;
		}
		busy = false;
	}

	function startReply() {
		replying = !replying;
		draft = '';
		if (replying) setTimeout(() => box?.focus());
	}
</script>

{#snippet who(x: Comment)}
	<div class="flex items-center gap-1.5 text-xs text-faint">
		{#if x.is_ai}
			<span class="chip chip-info px-1.5 py-0 font-mono text-[10px] font-semibold">
				{t('comments.ai')}
			</span>
		{/if}
		<span class="truncate font-semibold text-fg-strong">
			{x.mine ? t('comments.you') : x.author}
		</span>
		<span class="shrink-0">{formatDeadline(x.created_at)}</span>
		{#if x.can_delete}
			<button
				type="button"
				class="ml-auto shrink-0 text-faint hover:text-bad"
				title={t('comments.delete')}
				aria-label={t('comments.delete')}
				onclick={() => ondelete(x)}
			>
				<Icon icon="lucide:trash-2" width="13" />
			</button>
		{/if}
	</div>
{/snippet}

<article class="note rounded-lg border border-line bg-surface p-2.5 text-sm">
	{@render who(c)}
	<button
		type="button"
		class="mt-1.5 block w-full border-l-2 border-accent pl-2 text-left text-xs break-words text-muted hover:text-fg"
		title={anchored ? t('comments.locate') : t('comments.notFound')}
		disabled={!anchored}
		onclick={() => onlocate(c.quote)}
	>
		<span class="line-clamp-2">{c.quote}</span>
	</button>
	{#if c.is_ai}
		<div class="chat-md mt-1.5 text-sm text-fg">
			<!-- eslint-disable-next-line svelte/no-at-html-tags -- renderMarkdown sanitizes with DOMPurify against a narrow allow-list -->
			{@html renderMarkdown(c.body)}
		</div>
	{:else}
		<p class="mt-1.5 break-words whitespace-pre-wrap text-fg">{c.body}</p>
	{/if}
	{#if !anchored}
		<p class="mt-1 text-xs text-faint">{t('comments.notFound')}</p>
	{/if}

	{#each replies as r (r.id)}
		<div class="mt-2 border-l border-line pl-2">
			{@render who(r)}
			<p class="mt-0.5 break-words whitespace-pre-wrap text-fg">{r.body}</p>
		</div>
	{/each}

	{#if replying}
		<form
			class="mt-2 flex flex-col gap-1.5"
			onsubmit={(e) => {
				e.preventDefault();
				reply();
			}}
		>
			<textarea
				bind:this={box}
				bind:value={draft}
				onkeydown={(e) => {
					if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.isComposing) {
						e.preventDefault();
						reply();
					}
				}}
				rows="2"
				placeholder={t('comments.replyPlaceholder')}
				aria-label={t('comments.replyPlaceholder')}
				class="input min-h-[2.5rem] resize-none py-1.5 text-sm [field-sizing:content]"></textarea>
			<div class="flex gap-1.5">
				<button type="submit" class="btn btn-primary btn-sm" disabled={busy || !draft.trim()}>
					{t('comments.reply')}
				</button>
				<button type="button" class="btn btn-ghost btn-sm" onclick={startReply}>
					{t('comments.cancel')}
				</button>
			</div>
		</form>
	{:else}
		<button type="button" class="btn btn-ghost btn-sm mt-1 -ml-1.5" onclick={startReply}>
			<Icon icon="lucide:corner-down-right" width="13" />
			{t('comments.reply')}
		</button>
	{/if}
</article>
