<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import { superForm, defaults } from 'sveltekit-superforms';
	import { zod4 as zod, zod4Client as zodClient } from 'sveltekit-superforms/adapters';
	import { registerSchema } from '$lib/schemas/register';
	import { ApiError } from '$lib/api/http';
	import { register } from '$lib/api/auth';
	import { t } from '$lib/i18n/index.svelte';
	import AuthLayout from '$lib/components/AuthLayout.svelte';
	import Alert from '$lib/components/Alert.svelte';
	import RegisterFields from '$lib/components/RegisterFields.svelte';

	let { data } = $props();

	let errorMessage = $state<string | null>(null);

	const { form, errors, enhance, submitting } = superForm(defaults(zod(registerSchema)), {
		SPA: true,
		validators: zodClient(registerSchema),
		async onUpdate({ form }) {
			if (!form.valid) return;
			errorMessage = null;
			try {
				await register(form.data.username, form.data.nickname, form.data.password);
				await goto(resolve('/'));
			} catch (err) {
				if (err instanceof ApiError && err.status === 409) {
					errorMessage = t('register.usernameTaken');
				} else if (err instanceof ApiError && err.status === 403) {
					errorMessage = t('register.disabled');
				} else {
					errorMessage = t('register.failed');
				}
			}
		}
	});
</script>

<svelte:head><title>{t('register.title')} · Renderbin</title></svelte:head>

<AuthLayout>
	<h1 class="text-lg font-semibold text-fg-strong">{t('register.title')}</h1>
	{#if !data.allowRegistration}
		<p class="mt-1 mb-6 text-sm text-muted">{t('register.disabled')}</p>
		<a href={resolve('/login')} class="btn btn-primary h-10 w-full">
			<Icon icon="lucide:log-in" width="16" />
			{t('register.signIn')}
		</a>
	{:else}
		<p class="mt-1 mb-6 text-sm text-muted">{t('register.subtitle')}</p>

		<form method="POST" use:enhance class="flex flex-col gap-4">
			<RegisterFields {form} {errors} />

			<button type="submit" disabled={$submitting} class="btn btn-primary mt-1 h-10">
				<Icon icon="lucide:user-plus" width="16" />
				{$submitting ? t('register.submitting') : t('register.submit')}
			</button>
		</form>

		{#if errorMessage}
			<Alert class="mt-4">{errorMessage}</Alert>
		{/if}

		<p class="mt-6 text-center text-sm text-muted">
			{t('register.haveAccount')}
			<a href={resolve('/login')} class="font-medium text-accent hover:underline">
				{t('register.signIn')}
			</a>
		</p>
	{/if}
</AuthLayout>
