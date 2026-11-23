<script lang="ts">
	// The "Ask AI" window: one document, the reader's conversations about it,
	// and the one being had. It stays mounted while collapsed (the page hides
	// it), so an answer keeps streaming and a half-written question survives.
	import { tick } from 'svelte';
	import Alert from '$lib/components/Alert.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { errText } from '$lib/errors';
	import { formatDeadline } from '$lib/dates';
	import { renderMarkdown } from '$lib/markdown';
	import {
		deleteConversation,
		getConversation,
		sendChatMessage,
		type ChatFile,
		type Conversation
	} from '$lib/api/chat';

	let {
		slug,
		code,
		file,
		aiEnabled,
		initialConversations,
		oncollapse
	}: {
		slug: string;
		code: string | null;
		file: ChatFile;
		aiEnabled: boolean;
		initialConversations: Conversation[];
		oncollapse: () => void;
	} = $props();

	type Msg = {
		role: 'user' | 'assistant';
		content: string;
		/** An answer still being written. */
		pending?: boolean;
		/** An answer the reader stopped. */
		stopped?: boolean;
	};

	// svelte-ignore state_referenced_locally
	let conversations = $state<Conversation[]>(initialConversations);
	/** null is a new conversation, created by the server on its first answer. */
	let currentId = $state<number | null>(null);
	let messages = $state<Msg[]>([]);
	let input = $state('');
	let streaming = $state(false);
	let loading = $state(false);
	let error = $state<string | null>(null);
	let showHistory = $state(false);
	let abort: AbortController | null = null;
	let loadSeq = 0;

	let scroller = $state<HTMLElement>();
	let textarea = $state<HTMLTextAreaElement>();
	// Follow the answer as it grows, unless the reader has scrolled up to read.
	let stick = true;

	const current = $derived(conversations.find((c) => c.id === currentId));

	// Open the most recent conversation, if there is one.
	// svelte-ignore state_referenced_locally
	if (initialConversations.length) open(initialConversations[0].id);

	function onScroll() {
		if (!scroller) return;
		stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
	}

	async function scrollToEnd(force = false) {
		if (force) stick = true;
		if (!stick) return;
		await tick();
		if (scroller) scroller.scrollTop = scroller.scrollHeight;
	}

	$effect(() => {
		// Re-run on every new message and every streamed piece.
		void messages.length;
		void messages.at(-1)?.content;
		scrollToEnd();
	});

	async function open(id: number) {
		if (streaming) return;
		showHistory = false;
		error = null;
		currentId = id;
		messages = [];
		loading = true;
		const seq = ++loadSeq;
		try {
			const detail = await getConversation(slug, code, id);
			if (seq !== loadSeq) return;
			messages = detail.messages.map((m) => ({ role: m.role, content: m.content }));
			scrollToEnd(true);
		} catch (err) {
			if (seq === loadSeq) error = errText(err, 'error.chatLoad');
		} finally {
			if (seq === loadSeq) loading = false;
		}
	}

	function newChat() {
		if (streaming) return;
		loadSeq++;
		showHistory = false;
		error = null;
		currentId = null;
		messages = [];
		loading = false;
		textarea?.focus();
	}

	async function remove(c: Conversation) {
		if (!confirm(t('ai.confirmDelete', { title: c.title || t('ai.untitled') }))) return;
		try {
			await deleteConversation(slug, code, c.id);
			conversations = conversations.filter((x) => x.id !== c.id);
			if (currentId === c.id) newChat();
		} catch (err) {
			error = errText(err, 'error.chatDelete');
		}
	}

	async function send() {
		const text = input.trim();
		if (!text || streaming || !aiEnabled) return;
		error = null;
		messages.push(
			{ role: 'user', content: text },
			{ role: 'assistant', content: '', pending: true }
		);
		// The proxies the array now holds, so the stream below updates what renders.
		const asked = messages.length - 2;
		const answer = messages[messages.length - 1];
		input = '';
		streaming = true;
		scrollToEnd(true);
		let started = false;
		const controller = new AbortController();
		abort = controller;
		try {
			await sendChatMessage(
				slug,
				code,
				{ conversation_id: currentId, content: text },
				(ev) => {
					switch (ev.type) {
						case 'start':
							started = true;
							currentId = ev.conversation.id;
							conversations = [
								ev.conversation,
								...conversations.filter((c) => c.id !== ev.conversation.id)
							];
							break;
						case 'delta':
							answer.content += ev.text;
							break;
						case 'done':
							answer.pending = false;
							if (ev.message) answer.content = ev.message.content;
							break;
						case 'error':
							answer.pending = false;
							error = ev.error;
							break;
					}
				},
				controller.signal
			);
		} catch (err) {
			if (!started) {
				// Nothing was stored: the question did not happen. Put it back.
				messages.splice(asked, 2);
				if (!input) input = text;
				if (!controller.signal.aborted) error = errText(err, 'error.chatSend');
			} else if (controller.signal.aborted) {
				answer.stopped = true;
			} else {
				error = errText(err, 'error.chatSend');
			}
		} finally {
			answer.pending = false;
			if (!answer.content && started) messages.splice(messages.indexOf(answer), 1);
			streaming = false;
			abort = null;
		}
	}

	function stop() {
		abort?.abort();
	}

	function onKeydown(e: KeyboardEvent) {
		// Enter sends, Shift+Enter breaks the line. An IME composing Chinese also
		// reports Enter when a candidate is picked; that one is not a send.
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
			e.preventDefault();
			send();
		}
	}
</script>

<section class="flex h-full flex-col overflow-hidden bg-surface" aria-label={t('ai.ask')}>
	<!-- Header: title, history, new chat, collapse -->
	<header class="flex items-center gap-1 border-b border-line px-3 py-2">
		<Icon icon="lucide:sparkles" width="16" class="text-accent" />
		<div class="min-w-0 flex-1 px-1">
			<p class="truncate text-sm font-semibold text-fg-strong">
				{current ? current.title || t('ai.untitled') : t('ai.newChat')}
			</p>
			<p class="truncate text-xs text-muted">{file.name}</p>
		</div>
		<button
			class="btn btn-ghost btn-icon"
			class:bg-raised={showHistory}
			title={t('ai.history')}
			aria-label={t('ai.history')}
			aria-expanded={showHistory}
			onclick={() => (showHistory = !showHistory)}
		>
			<Icon icon="lucide:history" width="16" />
		</button>
		<button
			class="btn btn-ghost btn-icon"
			title={t('ai.newChat')}
			aria-label={t('ai.newChat')}
			disabled={streaming}
			onclick={newChat}
		>
			<Icon icon="lucide:square-pen" width="16" />
		</button>
		<button
			class="btn btn-ghost btn-icon"
			title={t('ai.collapse')}
			aria-label={t('ai.collapse')}
			onclick={oncollapse}
		>
			<Icon icon="lucide:panel-right-close" width="16" />
		</button>
	</header>

	<div class="relative min-h-0 flex-1">
		<!-- Conversation list, over the messages -->
		{#if showHistory}
			<div class="absolute inset-0 z-10 overflow-y-auto bg-surface p-2">
				{#if conversations.length === 0}
					<p class="p-4 text-center text-sm text-muted">{t('ai.noConversations')}</p>
				{/if}
				<ul class="flex flex-col gap-0.5">
					{#each conversations as c (c.id)}
						<li class="group flex items-center gap-1">
							<button
								class="min-w-0 flex-1 rounded-md px-3 py-2 text-left hover:bg-raised disabled:cursor-not-allowed"
								class:bg-raised={c.id === currentId}
								disabled={streaming}
								onclick={() => open(c.id)}
							>
								<span class="block truncate text-sm text-fg">{c.title || t('ai.untitled')}</span>
								<span class="block text-xs text-faint">{formatDeadline(c.updated_at)}</span>
							</button>
							<button
								class="btn btn-ghost btn-icon hover-bad opacity-60 group-hover:opacity-100"
								title={t('ai.deleteConversation')}
								aria-label={t('ai.deleteConversation')}
								disabled={streaming && c.id === currentId}
								onclick={() => remove(c)}
							>
								<Icon icon="lucide:trash-2" width="14" />
							</button>
						</li>
					{/each}
				</ul>
			</div>
		{/if}

		<!-- Messages -->
		<div bind:this={scroller} onscroll={onScroll} class="h-full overflow-y-auto px-4 py-4">
			{#if !aiEnabled}
				<Alert tone="warn">{t('ai.notConfigured')}</Alert>
			{:else if loading}
				<div class="flex justify-center py-8"><Spinner /></div>
			{:else if messages.length === 0}
				<div class="flex flex-col items-center gap-2 px-4 py-10 text-center">
					<Icon icon="lucide:sparkles" width="28" class="text-accent" />
					<p class="text-sm text-muted">{t('ai.empty', { name: file.name })}</p>
				</div>
			{/if}

			<div class="flex flex-col gap-4">
				{#each messages as m, i (i)}
					{#if m.role === 'user'}
						<div
							class="ml-8 self-end rounded-lg rounded-br-sm bg-raised px-3 py-2 text-sm break-words whitespace-pre-wrap text-fg"
						>
							{m.content}
						</div>
					{:else}
						<div class="chat-md mr-2 text-sm text-fg">
							{#if m.pending && !m.content}
								<span class="inline-flex items-center gap-2 text-muted">
									<Spinner />
									{t('ai.thinking')}
								</span>
							{:else}
								<!-- eslint-disable-next-line svelte/no-at-html-tags -- renderMarkdown sanitizes with DOMPurify against a narrow allow-list -->
								{@html renderMarkdown(m.content)}
								{#if m.stopped}
									<p class="text-xs text-faint">{t('ai.stopped')}</p>
								{/if}
							{/if}
						</div>
					{/if}
				{/each}
			</div>
		</div>
	</div>

	{#if error}
		<div class="px-3 pt-2">
			<Alert ondismiss={() => (error = null)}>{error}</Alert>
		</div>
	{/if}

	<!-- Composer -->
	<form
		class="flex items-end gap-2 border-t border-line p-3"
		onsubmit={(e) => {
			e.preventDefault();
			send();
		}}
	>
		<textarea
			bind:this={textarea}
			bind:value={input}
			onkeydown={onKeydown}
			rows="2"
			disabled={!aiEnabled}
			placeholder={t('ai.placeholder')}
			aria-label={t('ai.placeholder')}
			class="input max-h-40 min-h-[2.75rem] flex-1 resize-none py-2 [field-sizing:content]"
		></textarea>
		{#if streaming}
			<button type="button" class="btn btn-soft shrink-0" onclick={stop}>
				<Icon icon="lucide:square" width="14" />
				{t('ai.stop')}
			</button>
		{:else}
			<button type="submit" class="btn btn-primary shrink-0" disabled={!aiEnabled || !input.trim()}>
				<Icon icon="lucide:send-horizontal" width="14" />
				{t('ai.send')}
			</button>
		{/if}
	</form>
</section>
