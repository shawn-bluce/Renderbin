<script lang="ts">
	// A document with its comments beside it and, when a model is configured, an
	// "Ask AI" window floating over it. The document is framed from /res, so it
	// is still served under the sandbox and runs in an opaque origin: it can
	// neither reach this page nor make requests with the reader's session. This
	// page, in the app's own origin, holds the comments and the conversation.
	//
	// The document's injected script tells this page what was selected, where
	// passages are and how far it scrolled, through postMessage (see
	// $lib/selection.ts for why every message is treated as untrusted). Comment
	// opens a small input at the selection; Explain asks the model and the answer
	// becomes a comment with an AI mark. Both end up as notes floating over the
	// right edge, level with the passage they are about. The chat window is only
	// for asking the model questions, and starts closed: opening a document is
	// reading it.
	import { onMount } from 'svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { errText } from '$lib/errors';
	import {
		createComment,
		deleteComment,
		explainComment,
		listComments,
		type Comment
	} from '$lib/api/comments';
	import {
		explainAllowed,
		isPointerMessage,
		locateRequest,
		measureRequest,
		parseLocatedMessage,
		parseMeasuredMessage,
		parseScrollMessage,
		parseSelectionMessage,
		type Measured,
		type Rect
	} from '$lib/selection';
	import ChatPanel from './ChatPanel.svelte';
	import CommentPopover from './CommentPopover.svelte';
	import MarginNotes, { type Pending, type Thread } from './MarginNotes.svelte';

	let { data } = $props();

	const resUrl = $derived(
		`/res/${encodeURIComponent(data.slug)}` +
			(data.code ? `?code=${encodeURIComponent(data.code)}` : '')
	);
	const aiEnabled = $derived(data.overview.ai_enabled);

	// ---- comments -------------------------------------------------------
	let comments = $state<Comment[]>([]);
	let error = $state<string | null>(null);
	// The notes show by themselves once there are some; hiding them is the
	// reader's choice and sticks.
	let notesVisible = $state(false);
	let notesTouched = false;

	async function load() {
		try {
			comments = await listComments(data.slug, data.code);
			if (comments.length && !notesTouched) notesVisible = true;
		} catch (err) {
			error = errText(err, 'error.commentLoad');
		}
	}
	load();

	const threads = $derived<Thread[]>(
		comments
			.filter((c) => !c.parent_id)
			.map((c) => ({ c, replies: comments.filter((r) => r.parent_id === c.id) }))
	);

	function showNotes() {
		notesTouched = true;
		notesVisible = true;
	}

	async function reply(parent: Comment, body: string): Promise<boolean> {
		error = null;
		try {
			comments = [
				...comments,
				await createComment(data.slug, data.code, { parent_id: parent.id, body })
			];
			return true;
		} catch (err) {
			error = errText(err, 'error.commentPost');
			return false;
		}
	}

	async function remove(c: Comment) {
		const replies = c.parent_id ? 0 : comments.filter((r) => r.parent_id === c.id).length;
		const ask = replies
			? t('comments.confirmDeleteReplies', { count: replies })
			: t('comments.confirmDelete');
		if (!confirm(ask)) return;
		error = null;
		try {
			await deleteComment(data.slug, data.code, c.id);
			comments = comments.filter((x) => x.id !== c.id && x.parent_id !== c.id);
		} catch (err) {
			error = errText(err, 'error.commentDelete');
		}
	}

	// ---- the framed document -------------------------------------------
	let frame = $state<HTMLIFrameElement>();
	let scrollY = $state(0);
	let anchors = $state(new Map<number, Measured>());
	let timedOut = $state(false);
	let measuredOnce = false;

	function toFrame(message: unknown) {
		// The frame's origin is opaque and cannot be named as a target; every
		// message is a passage of the document it is already showing.
		frame?.contentWindow?.postMessage(message, '*');
	}

	// Ask the document where each comment's passage is, whenever the comments
	// change; it keeps the answer current as the layout changes, and highlights
	// each passage it finds. Hiding the notes asks for nothing, which also clears
	// the highlights.
	function measure() {
		toFrame(
			measureRequest(
				notesVisible ? threads.map(({ c }) => ({ id: c.id, text: c.quote, ai: c.is_ai })) : []
			)
		);
	}
	$effect(() => {
		void threads;
		void notesVisible;
		measure();
	});

	function onFrameLoad() {
		measure();
		// A document whose script never answers (nothing is injected into it)
		// would leave every note waiting; after a while they are listed as unplaced.
		setTimeout(() => {
			if (!measuredOnce) timedOut = true;
		}, 2500);
	}

	function locate(quote: string) {
		toFrame(locateRequest(quote));
	}

	// ---- the comment input at the selection ----------------------------
	let popover = $state<{ rect: Rect; quote: string } | null>(null);
	let popoverHasText = false;

	async function postComment(body: string): Promise<boolean> {
		if (!popover) return false;
		error = null;
		try {
			const c = await createComment(data.slug, data.code, { quote: popover.quote, body });
			comments = [...comments, c];
			popover = null;
			showNotes();
			return true;
		} catch (err) {
			error = errText(err, 'error.commentPost');
			return false;
		}
	}

	// ---- AI explanations -------------------------------------------------
	let pendings = $state<Pending[]>([]);
	let nextPending = 0;
	let lastExplain = -Infinity;

	async function explain(quote: string, rect: Rect) {
		const key = nextPending++;
		pendings.push({ key, quote, top: rect.y + scrollY, error: null });
		showNotes();
		try {
			const c = await explainComment(data.slug, data.code, quote);
			comments = [...comments, c];
			pendings = pendings.filter((p) => p.key !== key);
		} catch (err) {
			const p = pendings.find((x) => x.key === key);
			if (p) p.error = errText(err, 'error.explain');
		}
	}

	onMount(() => {
		function onMessage(e: MessageEvent) {
			// Only the framed document speaks to this page, and what it says is
			// validated field by field; anything else is ignored.
			if (!frame || e.source !== frame.contentWindow) return;
			const sel = parseSelectionMessage(e.data);
			if (sel) {
				if (sel.action === 'comment') {
					popoverHasText = false;
					popover = { rect: sel.rect, quote: sel.text };
				} else if (aiEnabled) {
					// "Explain" sends a question to the model on arrival, and this
					// message could come from the document's own script as well as
					// from the toolbar, so a burst is refused.
					const now = Date.now();
					if (!explainAllowed(now, lastExplain)) return;
					lastExplain = now;
					explain(sel.text, sel.rect);
				}
				return;
			}
			const measured = parseMeasuredMessage(e.data);
			if (measured) {
				measuredOnce = true;
				anchors = new Map(measured.map((m) => [m.id, m]));
				return;
			}
			const y = parseScrollMessage(e.data);
			if (y !== null) {
				scrollY = y;
				return;
			}
			if (isPointerMessage(e.data)) {
				// A click in the document closes an input nobody typed in.
				if (popover && !popoverHasText) popover = null;
				return;
			}
			const located = parseLocatedMessage(e.data);
			if (located && !located.ok) error = t('comments.notFound');
		}
		window.addEventListener('message', onMessage);
		return () => window.removeEventListener('message', onMessage);
	});

	// ---- the "Ask AI" window --------------------------------------------
	let chatOpen = $state(false);
</script>

<svelte:head>
	<title>{data.overview.file.name} · {aiEnabled ? t('ai.ask') : t('comments.title')}</title>
</svelte:head>

<iframe
	bind:this={frame}
	onload={onFrameLoad}
	src={resUrl}
	title={data.overview.file.name}
	class="fixed top-0 left-0 h-full w-full border-0 bg-white"
></iframe>

<!-- The comments float over the document at its right edge, level with their
     passages, like the "Ask AI" button; the button below shows or hides them. -->
{#if notesVisible}
	<MarginNotes
		{threads}
		{pendings}
		{anchors}
		{timedOut}
		{scrollY}
		onlocate={locate}
		onreply={reply}
		ondelete={remove}
		ondismiss={(key) => (pendings = pendings.filter((p) => p.key !== key))}
	/>
{/if}

{#if comments.length > 0}
	<button
		class="fixed right-4 bottom-[104px] z-40 flex h-9 items-center gap-1.5 rounded-full border border-line bg-surface px-3.5 text-[13px] font-semibold text-fg shadow-[0_1px_4px_rgba(0,0,0,0.2)] hover:border-line-strong"
		class:opacity-60={!notesVisible}
		aria-pressed={notesVisible}
		title={notesVisible ? t('comments.hide') : t('comments.show')}
		onclick={() => {
			notesTouched = true;
			notesVisible = !notesVisible;
		}}
	>
		<Icon icon="lucide:message-square" width="16" class="text-accent" />
		{t('comments.title')}
		<span class="font-normal text-faint">{threads.length}</span>
	</button>
{/if}

{#if popover}
	<CommentPopover
		rect={popover.rect}
		quote={popover.quote}
		maxRight={window.innerWidth}
		onsubmit={postComment}
		oncancel={() => (popover = null)}
		ondraft={(has) => (popoverHasText = has)}
	/>
{/if}

{#if error}
	<div
		class="fixed bottom-4 left-1/2 z-50 flex max-w-[min(32rem,calc(100vw-2rem))] -translate-x-1/2 items-center gap-2 rounded-lg border border-line bg-surface px-3 py-2 text-sm text-bad shadow-[0_6px_24px_rgba(0,0,0,0.22)]"
		role="alert"
	>
		<span class="min-w-0 flex-1 break-words">{error}</span>
		<button
			class="btn btn-ghost btn-icon"
			aria-label={t('comments.cancel')}
			onclick={() => (error = null)}
		>
			<Icon icon="lucide:x" width="14" />
		</button>
	</div>
{/if}

<!-- "Ask AI": a window for questions to the model. Hidden rather than unmounted
     when collapsed, so a streaming answer and a half-typed question survive it. -->
{#if aiEnabled}
	<div
		class="fixed inset-0 z-40 flex flex-col overflow-hidden border-line sm:inset-auto sm:top-4 sm:bottom-4 sm:w-[min(28rem,calc(100vw-2rem))] sm:rounded-xl sm:border sm:shadow-[0_6px_24px_rgba(0,0,0,0.22)] sm:right-4"
		class:hidden={!chatOpen}
	>
		<div class="min-h-0 flex-1">
			<ChatPanel
				slug={data.slug}
				code={data.code}
				file={data.overview.file}
				{aiEnabled}
				initialConversations={data.overview.conversations}
				oncollapse={() => (chatOpen = false)}
			/>
		</div>
		<!-- eslint-disable svelte/no-navigation-without-resolve -- /res is served by the Go backend, not a SvelteKit route -->
		<a
			href={resUrl}
			data-sveltekit-reload
			class="flex items-center justify-center gap-1.5 border-t border-line bg-surface py-1.5 text-xs text-muted hover:text-fg"
		>
			<Icon icon="lucide:external-link" width="12" />
			{t('ai.openOriginal')}
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	</div>

	{#if !chatOpen}
		<button
			class="fixed bottom-[60px] z-40 flex h-9 items-center gap-1.5 rounded-full border border-line bg-surface px-3.5 text-[13px] font-semibold text-fg shadow-[0_1px_4px_rgba(0,0,0,0.2)] right-4 hover:border-line-strong"
			onclick={() => (chatOpen = true)}
		>
			<Icon icon="lucide:sparkles" width="16" class="text-accent" />
			{t('ai.ask')}
		</button>
	{/if}
{/if}
