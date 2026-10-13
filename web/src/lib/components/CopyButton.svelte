<script lang="ts">
	import Icon from './Icon.svelte';
	import { copyFeedback } from '$lib/copy.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		text,
		title,
		labeled = false,
		class: className = 'btn btn-ghost btn-sm'
	}: { text: string; title?: string; labeled?: boolean; class?: string } = $props();

	const feedback = copyFeedback();
</script>

<button
	onclick={() => feedback.copy(text)}
	title={title ?? t('settings.copy')}
	aria-label={labeled ? undefined : (title ?? t('settings.copy'))}
	class={className}
>
	<Icon icon={feedback.is() ? 'lucide:check' : 'lucide:copy'} width="14" />
	{#if labeled}{feedback.is() ? t('settings.copied') : t('settings.copy')}{/if}
</button>
