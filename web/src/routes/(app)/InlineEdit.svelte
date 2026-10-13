<script lang="ts">
	// A text field that edits in place: Enter or the check saves, Escape or the
	// cross cancels. Renders three siblings, so the caller supplies the flex row.
	import Icon from '$lib/components/Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { focusOnMount } from '$lib/files-ui';

	let {
		value = $bindable(),
		label,
		placeholder,
		class: className = '',
		onsave,
		oncancel
	}: {
		value: string;
		label: string;
		placeholder?: string;
		class?: string;
		onsave: () => void;
		oncancel: () => void;
	} = $props();
</script>

<input
	bind:value
	use:focusOnMount
	{placeholder}
	aria-label={label}
	onkeydown={(e) => {
		if (e.key === 'Enter') onsave();
		if (e.key === 'Escape') oncancel();
	}}
	class="input h-7 {className}"
/>
<button
	onclick={onsave}
	title={t('common.save')}
	aria-label={t('common.save')}
	class="btn btn-ghost btn-icon size-7 text-ok"
>
	<Icon icon="lucide:check" width="14" />
</button>
<button
	onclick={oncancel}
	title={t('common.cancel')}
	aria-label={t('common.cancel')}
	class="btn btn-ghost btn-icon size-7"
>
	<Icon icon="lucide:x" width="14" />
</button>
