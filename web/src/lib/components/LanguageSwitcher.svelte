<script lang="ts">
	import Icon from './Icon.svelte';
	import { getLocale, setLocale, t } from '$lib/i18n/index.svelte';

	// One button that flips between the two languages and names the current one.
</script>

<button
	onclick={() => setLocale(getLocale() === 'zh' ? 'en' : 'zh')}
	title={t('switcher.label')}
	aria-label={t('switcher.label')}
	class="btn btn-ghost btn-sm gap-1 px-2"
>
	<Icon icon="lucide:languages" width="16" />
	<span class="text-xs font-semibold">{getLocale() === 'zh' ? '中' : 'EN'}</span>
</button>
