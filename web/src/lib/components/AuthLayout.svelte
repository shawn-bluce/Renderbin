<script lang="ts">
	// Frame for the signed-out pages (login, register, welcome): one centered
	// card, with the theme and language switches reachable before anyone has
	// logged in.
	import type { Snippet } from 'svelte';
	import Brand from './Brand.svelte';
	import ThemeToggle from './ThemeToggle.svelte';
	import LanguageSwitcher from './LanguageSwitcher.svelte';

	let { children, wide = false }: { children: Snippet; wide?: boolean } = $props();
</script>

<main class="relative flex min-h-dvh items-center justify-center bg-page p-4 pt-14 sm:p-6">
	<div class="absolute top-3 right-3 flex items-center">
		<LanguageSwitcher />
		<ThemeToggle />
	</div>
	<div class="w-full {wide ? 'max-w-lg' : 'max-w-sm'}">
		<div class="mb-6 flex justify-center"><Brand size={34} /></div>
		<div class="card p-6 sm:p-8">
			{@render children()}
		</div>
	</div>
</main>
