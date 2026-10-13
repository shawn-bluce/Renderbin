<script lang="ts">
	// A labelled text input for the superforms pages. `errors` is the field's
	// slice of superforms' error store: validation messages are i18n keys (see the
	// zod schemas), translated here, once.
	import { t } from '$lib/i18n/index.svelte';
	import type { MessageKey } from '$lib/i18n/messages';

	let {
		label,
		name,
		value = $bindable(),
		errors,
		type = 'text',
		autocomplete
	}: {
		label: string;
		name: string;
		value: string | undefined;
		errors?: string[];
		type?: 'text' | 'password';
		autocomplete?: 'username' | 'nickname' | 'current-password' | 'new-password';
	} = $props();
</script>

<label class="field">
	<span class="label">{label}</span>
	<input {name} {type} {autocomplete} bind:value class="input input-lg" />
	{#if errors?.[0]}
		<span class="text-xs text-bad">{t(errors[0] as MessageKey)}</span>
	{/if}
</label>
