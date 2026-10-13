<script lang="ts">
	import Icon from './Icon.svelte';
	import { getTheme, toggleTheme } from '$lib/theme.svelte';
	import { t } from '$lib/i18n/index.svelte';

	// Shows the theme you would switch TO, as the icon on a light switch does.
	const label = $derived(getTheme() === 'dark' ? t('theme.toLight') : t('theme.toDark'));
</script>

<button onclick={toggleTheme} title={label} aria-label={label} class="btn btn-ghost btn-icon">
	<Icon icon={getTheme() === 'dark' ? 'lucide:sun' : 'lucide:moon'} width="16" />
</button>
