<script lang="ts">
	// The four fields of "create an account" -- shared by /register and the
	// first-run /welcome page, which validate against the same schema.
	import type { Readable, Writable } from 'svelte/store';
	import FormField from './FormField.svelte';
	import { t } from '$lib/i18n/index.svelte';

	type Fields = { username: string; nickname: string; password: string; rePassword: string };

	let {
		form,
		errors
	}: {
		form: Writable<Fields>;
		errors: Readable<{ [K in keyof Fields]?: string[] }>;
	} = $props();
</script>

<FormField
	label={t('login.username')}
	name="username"
	autocomplete="username"
	bind:value={$form.username}
	errors={$errors.username}
/>
<FormField
	label={t('register.nickname')}
	name="nickname"
	autocomplete="nickname"
	bind:value={$form.nickname}
	errors={$errors.nickname}
/>
<FormField
	label={t('login.password')}
	name="password"
	type="password"
	autocomplete="new-password"
	bind:value={$form.password}
	errors={$errors.password}
/>
<FormField
	label={t('register.rePassword')}
	name="rePassword"
	type="password"
	autocomplete="new-password"
	bind:value={$form.rePassword}
	errors={$errors.rePassword}
/>
