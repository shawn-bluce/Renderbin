<script lang="ts">
	// A dialog on the platform's own <dialog>: top layer, focus trap, inert
	// background and Escape all come for free. On phones it docks to the bottom
	// edge as a sheet; from `sm` up it is centered.
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';
	import Spinner from './Spinner.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		title,
		onclose,
		busy = false,
		wide = false,
		onsubmit,
		submitLabel,
		busyLabel,
		submitIcon,
		submitDisabled = false,
		showCancel = true,
		tone = 'primary',
		children,
		footer
	}: {
		title: string;
		onclose: () => void;
		// While true the dialog cannot be dismissed (a request is in flight).
		busy?: boolean;
		wide?: boolean;
		// With `onsubmit` the dialog gets the usual footer: Cancel, then one action
		// button that shows a spinner and `busyLabel` while `busy`.
		onsubmit?: () => void;
		submitLabel?: string;
		busyLabel?: string;
		submitIcon?: string;
		submitDisabled?: boolean;
		showCancel?: boolean;
		tone?: 'primary' | 'danger';
		children: Snippet;
		// Replaces the default footer, for dialogs whose buttons change (a "done" state).
		footer?: Snippet;
	} = $props();

	function open(node: HTMLDialogElement) {
		node.showModal();
		return () => node.close();
	}

	function tryClose() {
		if (!busy) onclose();
	}
</script>

<!-- A click that lands on the <dialog> itself, not on its card, hit the backdrop. -->
<dialog
	{@attach open}
	class="modal {wide ? 'sm:max-w-3xl' : ''}"
	aria-label={title}
	oncancel={(e) => {
		e.preventDefault();
		tryClose();
	}}
	onclick={(e) => e.target === e.currentTarget && tryClose()}
>
	<div
		class="card flex max-h-[92dvh] flex-col shadow-2xl shadow-black/50 max-sm:rounded-b-none sm:max-h-[88dvh]"
	>
		<header class="flex items-center justify-between gap-3 border-b border-line px-5 py-3.5">
			<h2 class="text-base font-semibold text-fg-strong">{title}</h2>
			<button
				onclick={tryClose}
				disabled={busy}
				title={t('common.close')}
				aria-label={t('common.close')}
				class="btn btn-ghost btn-icon -mr-1.5"
			>
				<Icon icon="lucide:x" width="18" />
			</button>
		</header>
		<div class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
			{@render children()}
		</div>
		{#if footer || onsubmit}
			<footer
				class="flex justify-end gap-2 border-t border-line px-5 py-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]"
			>
				{#if footer}
					{@render footer()}
				{:else}
					{#if showCancel}
						<button onclick={tryClose} disabled={busy} class="btn btn-ghost">
							{t('common.cancel')}
						</button>
					{/if}
					<button
						onclick={onsubmit}
						disabled={busy || submitDisabled}
						class="btn {tone === 'danger' ? 'btn-danger' : 'btn-primary'}"
					>
						{#if busy}
							<Spinner />
						{:else if submitIcon}
							<Icon icon={submitIcon} width="15" />
						{/if}
						{busy ? (busyLabel ?? t('common.saving')) : (submitLabel ?? t('common.save'))}
					</button>
				{/if}
			</footer>
		{/if}
	</div>
</dialog>
