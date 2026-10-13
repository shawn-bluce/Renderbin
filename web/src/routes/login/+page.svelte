<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import { superForm, defaults } from 'sveltekit-superforms';
	import { zod4 as zod, zod4Client as zodClient } from 'sveltekit-superforms/adapters';
	import { loginSchema } from '$lib/schemas/login';
	import { ApiError } from '$lib/api/http';
	import { login } from '$lib/api/auth';
	import { t } from '$lib/i18n/index.svelte';
	import AuthLayout from '$lib/components/AuthLayout.svelte';
	import Alert from '$lib/components/Alert.svelte';
	import FormField from '$lib/components/FormField.svelte';

	let { data } = $props();

	let errorMessage = $state<string | null>(null);

	const { form, errors, enhance, submitting } = superForm(defaults(zod(loginSchema)), {
		SPA: true,
		validators: zodClient(loginSchema),
		async onUpdate({ form }) {
			if (!form.valid) return;
			errorMessage = null;
			try {
				await login(form.data.username, form.data.password);
				await goto(resolve('/'));
			} catch (err) {
				// 403 means the credentials were correct but the account is
				// suspended; showing "wrong password" there would have someone
				// retyping a password that works.
				errorMessage =
					err instanceof ApiError && err.status === 403
						? t('login.accountDisabled')
						: t('login.invalidCredentials');
			}
		}
	});
</script>

<svelte:head><title>{t('login.signIn')} · Renderbin</title></svelte:head>

<AuthLayout>
	<h1 class="text-lg font-semibold text-fg-strong">{t('login.signIn')}</h1>
	<p class="mt-1 mb-6 text-sm text-muted">{t('login.subtitle')}</p>

	<form method="POST" use:enhance class="flex flex-col gap-4">
		<FormField
			label={t('login.username')}
			name="username"
			autocomplete="username"
			bind:value={$form.username}
			errors={$errors.username}
		/>
		<FormField
			label={t('login.password')}
			name="password"
			type="password"
			autocomplete="current-password"
			bind:value={$form.password}
			errors={$errors.password}
		/>

		<button type="submit" disabled={$submitting} class="btn btn-primary mt-1 h-10">
			<Icon icon="lucide:log-in" width="16" />
			{$submitting ? t('login.signingIn') : t('login.signIn')}
		</button>
	</form>

	{#if errorMessage}
		<Alert class="mt-4">{errorMessage}</Alert>
	{/if}

	{#if data.allowRegistration}
		<p class="mt-6 text-center text-sm text-muted">
			{t('login.noAccount')}
			<a href={resolve('/register')} class="font-medium text-accent hover:underline">
				{t('login.register')}
			</a>
		</p>
	{/if}
</AuthLayout>
