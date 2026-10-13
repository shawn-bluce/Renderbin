<script lang="ts">
	import Icon from './Icon.svelte';

	let { icon, label }: { icon: string; label: string } = $props();
</script>

<h2 class="flex items-center gap-2 text-base font-semibold text-fg-strong">
	<Icon {icon} width="17" class="text-accent" />
	{label}
</h2>
