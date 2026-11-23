<script lang="ts">
	// A document with "Ask AI" beside it. The document is framed from /res, so
	// it is still served under the sandbox and runs in an opaque origin: it can
	// neither reach this page nor make requests with the reader's session. This
	// page, in the app's own origin, holds the conversation. (The /res page's own
	// Ask AI and QR buttons hide themselves inside a frame.)
	import Icon from '$lib/components/Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import ChatPanel from './ChatPanel.svelte';

	let { data } = $props();

	const resUrl = $derived(
		`/res/${encodeURIComponent(data.slug)}` +
			(data.code ? `?code=${encodeURIComponent(data.code)}` : '')
	);

	// Arriving here is asking to chat, so the window starts open.
	let open = $state(true);
</script>

<svelte:head>
	<title>{data.overview.file.name} · {t('ai.ask')}</title>
</svelte:head>

<iframe
	src={resUrl}
	title={data.overview.file.name}
	class="fixed inset-0 size-full border-0 bg-white"
></iframe>

<!-- The window: full screen on a phone, a floating panel on the right from sm
     up. Hidden rather than unmounted when collapsed, so a streaming answer and
     a half-typed question survive it. -->
<div
	class="fixed inset-0 z-40 flex flex-col overflow-hidden border-line sm:inset-auto sm:top-4 sm:right-4 sm:bottom-4 sm:w-[min(28rem,calc(100vw-2rem))] sm:rounded-xl sm:border sm:shadow-[0_6px_24px_rgba(0,0,0,0.22)]"
	class:hidden={!open}
>
	<ChatPanel
		slug={data.slug}
		code={data.code}
		file={data.overview.file}
		aiEnabled={data.overview.ai_enabled}
		initialConversations={data.overview.conversations}
		oncollapse={() => (open = false)}
	/>
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

{#if !open}
	<!-- Where the /res page's own button sat, so it is found again in place. -->
	<button
		class="fixed right-4 bottom-[60px] z-40 flex h-9 items-center gap-1.5 rounded-full border border-line bg-surface px-3.5 text-[13px] font-semibold text-fg shadow-[0_1px_4px_rgba(0,0,0,0.2)] hover:border-line-strong"
		onclick={() => (open = true)}
	>
		<Icon icon="lucide:sparkles" width="16" class="text-accent" />
		{t('ai.ask')}
	</button>
{/if}
