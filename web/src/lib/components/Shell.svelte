<script lang="ts">
	// The signed-in frame shared by the dashboard and settings: a fixed sidebar on
	// wide screens (so the content can use the rest of the width), a sticky top
	// bar on narrow ones. The page supplies the middle of it through `nav`, which
	// is rendered once per layout and told which one it is.
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from './Icon.svelte';
	import Brand from './Brand.svelte';
	import ThemeToggle from './ThemeToggle.svelte';
	import LanguageSwitcher from './LanguageSwitcher.svelte';
	import { logout } from '$lib/api/auth';
	import type { Me } from '$lib/api/auth';
	import { t } from '$lib/i18n/index.svelte';

	let {
		user,
		nav,
		children
	}: {
		user: Me;
		nav?: Snippet<[boolean]>;
		children: Snippet;
	} = $props();

	async function onLogout() {
		await logout();
		await goto(resolve('/login'));
	}
</script>

{#snippet tools()}
	<LanguageSwitcher />
	<ThemeToggle />
	<a
		href={resolve('/settings')}
		title={t('header.settings')}
		aria-label={t('header.settings')}
		class="btn btn-ghost btn-icon"
	>
		<Icon icon="lucide:settings" width="16" />
	</a>
	<button
		onclick={onLogout}
		title={t('header.logout')}
		aria-label={t('header.logout')}
		class="btn btn-ghost btn-icon hover-bad"
	>
		<Icon icon="lucide:log-out" width="16" />
	</button>
{/snippet}

<div class="min-h-dvh lg:grid lg:grid-cols-[15rem_minmax(0,1fr)]">
	<aside
		class="sticky top-0 hidden h-dvh flex-col gap-5 border-r border-line bg-surface px-3 py-4 lg:flex"
	>
		<a href={resolve('/')} class="px-2" aria-label="Renderbin"><Brand /></a>
		<nav class="min-h-0 flex-1 overflow-y-auto">
			{@render nav?.(false)}
		</nav>
		<div class="flex flex-col gap-2 border-t border-line pt-3">
			<!-- The nickname, not the username: it is the name the account chose to
			     go by, and the username is a login credential with no reason to be
			     on every screen. -->
			<div class="flex items-center gap-2.5 px-2" title={user.nickname}>
				<span
					class="flex size-7 shrink-0 items-center justify-center rounded-full bg-raised text-xs font-semibold text-fg-strong uppercase"
				>
					{[...user.nickname][0] ?? '?'}
				</span>
				<span class="min-w-0 flex-1 truncate text-sm font-medium text-fg-strong">
					{user.nickname}
				</span>
				{#if user.is_admin}
					<span class="chip chip-warn">{t('shell.admin')}</span>
				{/if}
			</div>
			<div class="flex items-center justify-between">
				{@render tools()}
			</div>
		</div>
	</aside>

	<div class="min-w-0">
		<header class="sticky top-0 z-20 border-b border-line bg-surface lg:hidden">
			<div class="flex items-center justify-between gap-2 px-3 py-2">
				<a href={resolve('/')} aria-label="Renderbin"><Brand size={26} /></a>
				<div class="flex items-center">
					{@render tools()}
				</div>
			</div>
			{#if nav}
				<div class="overflow-x-auto px-3 pb-2">
					{@render nav(true)}
				</div>
			{/if}
		</header>

		{@render children()}
	</div>
</div>
