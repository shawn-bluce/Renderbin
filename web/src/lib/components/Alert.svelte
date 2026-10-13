<script lang="ts">
	// A boxed message. Errors and warnings are announced at once (role=alert); a
	// success is polite (role=status) so it never interrupts what the reader is doing.
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';

	const defaultIcon = {
		bad: 'lucide:alert-circle',
		warn: 'lucide:alert-triangle',
		ok: 'lucide:check-circle-2'
	} as const;

	let {
		tone = 'bad',
		icon,
		iconClass = '',
		ondismiss,
		class: className = '',
		children
	}: {
		tone?: 'bad' | 'warn' | 'ok';
		icon?: string;
		iconClass?: string;
		// Shows a close button when given.
		ondismiss?: () => void;
		class?: string;
		children: Snippet;
	} = $props();
</script>

<div role={tone === 'ok' ? 'status' : 'alert'} class="alert alert-{tone} {className}">
	<Icon icon={icon ?? defaultIcon[tone]} width="16" class={iconClass} />
	<div class="min-w-0 flex-1 break-words">{@render children()}</div>
	{#if ondismiss}
		<button
			onclick={ondismiss}
			title={t('copy.dismiss')}
			aria-label={t('copy.dismiss')}
			class="-my-0.5 rounded p-0.5 hover:bg-current/15"
		>
			<Icon icon="lucide:x" width="14" />
		</button>
	{/if}
</div>
