<script lang="ts">
	import Icon from './Icon.svelte';

	let { width = 15, class: className = '' }: { width?: number; class?: string } = $props();
</script>

<Icon icon="lucide:loader-2" {width} class="animate-spin {className}" />
