<script lang="ts">
	// Account management, rendered inline in Settings (super admin only). Lives
	// beside +page.svelte rather than in $lib/components because it is one page's
	// section, not shared UI — SvelteKit only treats +-prefixed files as routes,
	// so a plain component here is not reachable as a URL.
	import Alert from '$lib/components/Alert.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import SectionTitle from '$lib/components/SectionTitle.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import {
		createUser,
		deleteUser,
		listUsers,
		resetUserPassword,
		setUserDisabled,
		setUserQuota,
		type AdminUser,
		type CreatedUser
	} from '$lib/api/admin';
	import { formatDate } from '$lib/dates';
	import { errText } from '$lib/errors';
	import { formatSize, parseSize } from '$lib/format';
	import { MIN_PASSWORD } from '$lib/schemas/register';

	let { users: initial }: { users: AdminUser[] } = $props();

	// Rows are mutated locally after each successful call: the status endpoint
	// answers 204, since the row it could return wouldn't carry the file counts
	// shown here.
	// svelte-ignore state_referenced_locally
	let users = $state<AdminUser[]>(initial);

	let errorMessage = $state<string | null>(null);
	let successMessage = $state<string | null>(null);
	let busyId = $state<number | null>(null);

	// Reset-password modal.
	let resetUser = $state<AdminUser | null>(null);
	let resetPassword = $state('');
	let resetBusy = $state(false);
	let resetError = $state<string | null>(null);

	// Create-account modal, and the credentials it hands back exactly once.
	let createOpen = $state(false);
	let createUsername = $state('');
	let createNickname = $state('');
	let createBusy = $state(false);
	let createError = $state<string | null>(null);
	let createdUser = $state<CreatedUser | null>(null);

	// Delete-account modal. Deleting takes the account's files with it and does
	// not go through the trash, so this is a modal spelling out the counts
	// rather than a one-line confirm().
	let deleteUserTarget = $state<AdminUser | null>(null);
	let deleteBusy = $state(false);
	let deleteError = $state<string | null>(null);

	// Inline quota editing, one row at a time.
	let quotaEditId = $state<number | null>(null);
	let quotaInput = $state('');
	let quotaBusy = $state(false);

	async function toggleDisabled(user: AdminUser) {
		// Disabling signs someone out and breaks their links, so it asks first;
		// re-enabling only restores access and needs no confirmation.
		if (!user.disabled && !confirm(t('confirm.disableAccount', { name: user.nickname }))) return;
		errorMessage = null;
		successMessage = null;
		busyId = user.id;
		try {
			await setUserDisabled(user.id, !user.disabled);
			users = users.map((u) => (u.id === user.id ? { ...u, disabled: !user.disabled } : u));
		} catch {
			errorMessage = t('error.accountStatus');
		} finally {
			busyId = null;
		}
	}

	function openReset(user: AdminUser) {
		resetUser = user;
		resetPassword = '';
		resetError = null;
	}

	function closeReset() {
		resetUser = null;
	}

	async function submitReset() {
		if (!resetUser) return;
		if (resetPassword.length < MIN_PASSWORD) {
			resetError = t('register.passwordTooShort');
			return;
		}
		resetError = null;
		resetBusy = true;
		const name = resetUser.nickname;
		try {
			await resetUserPassword(resetUser.id, resetPassword);
			successMessage = t('accounts.resetDone', { name });
			errorMessage = null;
			closeReset();
		} catch (err) {
			resetError = errText(err, 'error.resetPassword');
		} finally {
			resetBusy = false;
		}
	}

	function openCreate() {
		createOpen = true;
		createUsername = '';
		createNickname = '';
		createError = null;
		createdUser = null;
	}

	function closeCreate() {
		createOpen = false;
		createdUser = null;
	}

	async function submitCreate() {
		const username = createUsername.trim();
		const nickname = createNickname.trim() || username;
		if (!username) {
			createError = t('accounts.usernameRequired');
			return;
		}
		createError = null;
		createBusy = true;
		try {
			const created = await createUser(username, nickname);
			// The password is in this response and nowhere else, so the modal
			// switches to showing it instead of closing.
			createdUser = created;
			// The list comes from the server so the new row carries its real
			// defaults (quota, join date) rather than a guess at them.
			users = await listUsers().catch(() => users);
		} catch (err) {
			createError = errText(err, 'error.createAccount');
		} finally {
			createBusy = false;
		}
	}

	function openDelete(user: AdminUser) {
		deleteUserTarget = user;
		deleteError = null;
	}

	function closeDelete() {
		deleteUserTarget = null;
	}

	async function submitDelete() {
		if (!deleteUserTarget) return;
		const target = deleteUserTarget;
		deleteError = null;
		deleteBusy = true;
		try {
			const { deleted_files } = await deleteUser(target.id);
			users = users.filter((u) => u.id !== target.id);
			successMessage = t('accounts.deleteDone', { name: target.nickname, n: deleted_files });
			errorMessage = null;
			closeDelete();
		} catch (err) {
			deleteError = errText(err, 'error.deleteAccount');
		} finally {
			deleteBusy = false;
		}
	}

	function startQuotaEdit(user: AdminUser) {
		quotaEditId = user.id;
		// Pre-filled with the same string the row displays, unit and all, so the
		// field and the label can never disagree. Rounding this into bare
		// megabytes turned any sub-megabyte quota into "0", and then merely
		// opening the editor and clicking away set the account's limit to zero.
		quotaInput = formatSize(user.quota_bytes);
	}

	function cancelQuotaEdit() {
		quotaEditId = null;
		quotaInput = '';
	}

	async function submitQuota(user: AdminUser) {
		// Enter fires this, then sets `disabled` on the still-focused input --
		// and the HTML spec has the browser blur an element that becomes
		// disabled, which fires onblur and would submit the same value twice.
		if (quotaBusy) return;

		const bytes = parseSize(quotaInput);
		if (bytes === null) {
			errorMessage = t('error.quotaInvalid');
			cancelQuotaEdit();
			return;
		}
		if (bytes === user.quota_bytes) {
			cancelQuotaEdit();
			return;
		}
		errorMessage = null;
		successMessage = null;
		quotaBusy = true;
		try {
			await setUserQuota(user.id, bytes);
			users = users.map((u) => (u.id === user.id ? { ...u, quota_bytes: bytes } : u));
		} catch (err) {
			errorMessage = errText(err, 'error.quota');
		} finally {
			quotaBusy = false;
			// Only close the editor if it is still this row's. Clicking straight
			// from one row's quota to another's blurs the first (starting this
			// call) before opening the second, so an unguarded close would shut
			// the editor the user just opened, tens of milliseconds later.
			if (quotaEditId === user.id) cancelQuotaEdit();
		}
	}
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape' && quotaEditId !== null && !quotaBusy) cancelQuotaEdit();
	}}
/>

<section id="accounts" class="card scroll-mt-20 p-5">
	<div class="flex flex-wrap items-start justify-between gap-3">
		<div>
			<SectionTitle icon="lucide:users" label={t('accounts.title')} />
			<p class="hint">{t('accounts.subtitle')}</p>
		</div>
		<!-- Adding a colleague used to mean turning global self-registration on,
		     having them sign up, and turning it back off. -->
		<button onclick={openCreate} class="btn btn-soft btn-sm">
			<Icon icon="lucide:user-plus" width="14" />
			{t('accounts.create')}
		</button>
	</div>

	{#if errorMessage}
		<Alert class="mt-4">{errorMessage}</Alert>
	{/if}
	{#if successMessage}
		<Alert tone="ok" class="mt-4">{successMessage}</Alert>
	{/if}

	<ul class="panel mt-4 overflow-hidden">
		{#each users as user (user.id)}
			<li
				class="flex flex-col gap-3 border-b border-line px-3 py-3 last:border-0 lg:flex-row lg:items-center"
			>
				<div class="flex min-w-0 items-center gap-3 lg:w-72 lg:shrink-0">
					<span
						class="flex size-8 shrink-0 items-center justify-center rounded-md {user.disabled
							? 'bg-raised text-faint'
							: 'bg-ok/15 text-ok'}"
					>
						<Icon icon={user.is_super_admin ? 'lucide:shield-check' : 'lucide:user'} width="15" />
					</span>
					<div class="min-w-0">
						<div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
							<span
								class="truncate text-sm font-medium {user.disabled
									? 'text-faint line-through'
									: 'text-fg-strong'}"
							>
								{user.nickname}
							</span>
							{#if user.is_super_admin}
								<span class="chip chip-ok" title={t('accounts.superAdminLocked')}>
									{t('accounts.superAdmin')}
								</span>
							{/if}
							{#if user.disabled}
								<span class="chip chip-bad" title={t('accounts.disabledNote')}>
									{t('accounts.disabledBadge')}
								</span>
							{/if}
						</div>
						<span class="font-mono text-xs text-muted">@{user.username}</span>
					</div>
				</div>

				<div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
					<span class="flex items-center gap-1">
						<Icon icon="lucide:file-code-2" width="12" />
						{t('accounts.files', { n: user.file_count })}
					</span>
					{#if user.trashed_count > 0}
						<span class="flex items-center gap-1">
							<Icon icon="lucide:trash-2" width="12" />
							{t('accounts.trashed', { n: user.trashed_count })}
						</span>
					{/if}
					{#if quotaEditId === user.id}
						<span class="flex items-center gap-1">
							<Icon icon="lucide:hard-drive" width="12" />
							<!-- svelte-ignore a11y_autofocus -->
							<input
								type="text"
								autofocus
								bind:value={quotaInput}
								disabled={quotaBusy}
								aria-label={t('accounts.quotaLabel')}
								placeholder={t('accounts.quotaUnitHint')}
								onkeydown={(e) => {
									if (e.key === 'Enter') submitQuota(user);
								}}
								onblur={() => submitQuota(user)}
								class="input h-7 w-24 px-1.5 font-mono text-xs"
							/>
							<button
								onmousedown={(e) => e.preventDefault()}
								onclick={cancelQuotaEdit}
								aria-label={t('common.cancel')}
								title={t('common.cancel')}
								class="btn btn-ghost size-6 p-0"
							>
								<Icon icon="lucide:x" width="12" />
							</button>
						</span>
					{:else}
						<button
							onclick={() => startQuotaEdit(user)}
							title={t('accounts.quotaEdit')}
							class="-mx-1 flex items-center gap-1 rounded px-1 transition-colors hover:bg-raised hover:text-fg {user.used_bytes >
							user.quota_bytes
								? 'text-bad'
								: ''}"
						>
							<Icon icon="lucide:hard-drive" width="12" />
							<span class="font-mono"
								>{formatSize(user.used_bytes)} / {formatSize(user.quota_bytes)}</span
							>
						</button>
					{/if}
					<span>{t('accounts.created', { date: formatDate(user.created_at) })}</span>
				</div>

				<div class="flex shrink-0 flex-wrap items-center gap-1.5">
					<button onclick={() => openReset(user)} class="btn btn-soft btn-sm">
						<Icon icon="lucide:key-round" width="13" />
						{t('accounts.resetPassword')}
					</button>
					<!-- The super admin is the only account that can lift a suspension,
					     so it cannot suspend itself; the server refuses this too. -->
					<button
						onclick={() => toggleDisabled(user)}
						disabled={user.is_super_admin || busyId === user.id}
						title={user.is_super_admin
							? t('accounts.superAdminLocked')
							: user.disabled
								? t('accounts.enableTitle')
								: t('accounts.disableTitle')}
						class="btn btn-soft btn-sm {user.disabled ? 'text-ok' : 'hover-bad'}"
					>
						<Icon icon={user.disabled ? 'lucide:user-check' : 'lucide:user-x'} width="13" />
						{user.disabled ? t('accounts.enable') : t('accounts.disable')}
					</button>
					<!-- Deleting the super admin would remove the only account that can
					     manage accounts, so it is refused here and on the server. -->
					<button
						onclick={() => openDelete(user)}
						disabled={user.is_super_admin}
						title={user.is_super_admin ? t('accounts.superAdminLocked') : t('accounts.deleteTitle')}
						aria-label={t('accounts.deleteTitle')}
						class="btn btn-soft btn-sm btn-icon hover-bad"
					>
						<Icon icon="lucide:trash-2" width="13" />
					</button>
				</div>
			</li>
		{/each}
	</ul>
</section>

{#if resetUser}
	<Modal
		title={t('accounts.resetTitle')}
		onclose={closeReset}
		busy={resetBusy}
		onsubmit={submitReset}
		submitLabel={t('accounts.resetPassword')}
	>
		<p class="text-sm text-muted">
			{t('accounts.resetBody', { name: resetUser.nickname })}
		</p>

		<label class="field">
			<span class="label">{t('accounts.newPassword')}</span>
			<input
				type="password"
				bind:value={resetPassword}
				autocomplete="new-password"
				onkeydown={(e) => {
					if (e.key === 'Enter') submitReset();
				}}
				class="input"
			/>
		</label>

		{#if resetError}
			<Alert>{resetError}</Alert>
		{/if}
	</Modal>
{/if}

{#if createOpen}
	<Modal
		title={t('accounts.createTitle')}
		onclose={closeCreate}
		busy={createBusy}
		onsubmit={createdUser ? closeCreate : submitCreate}
		submitLabel={createdUser ? t('common.done') : t('accounts.create')}
		busyLabel={t('common.saving')}
		showCancel={!createdUser}
	>
		{#if createdUser}
			<!-- The generated password exists only in this response. Nothing
			     stores it in plaintext, so once this modal closes the only way
			     to give the account a known password is to reset it. -->
			<p class="text-sm text-muted">
				{t('accounts.createdBody', { name: createdUser.nickname })}
			</p>
			<div class="panel flex flex-col gap-1.5 p-3">
				<span class="text-xs text-muted">@{createdUser.username}</span>
				<div class="flex items-center gap-2">
					<code class="min-w-0 flex-1 truncate font-mono text-sm text-ok"
						>{createdUser.password}</code
					>
					<CopyButton
						text={createdUser.password}
						title={t('accounts.copyPassword')}
						class="btn btn-soft btn-icon"
					/>
				</div>
			</div>
			<p class="text-xs text-warn">{t('accounts.createdWarning')}</p>
		{:else}
			<p class="text-sm text-muted">{t('accounts.createBody')}</p>

			<label class="field">
				<span class="label">{t('accounts.username')}</span>
				<input type="text" bind:value={createUsername} autocomplete="off" class="input font-mono" />
			</label>
			<label class="field">
				<span class="label">{t('accounts.nickname')}</span>
				<input
					type="text"
					bind:value={createNickname}
					autocomplete="off"
					placeholder={createUsername}
					onkeydown={(e) => {
						if (e.key === 'Enter') submitCreate();
					}}
					class="input"
				/>
			</label>

			{#if createError}
				<Alert>{createError}</Alert>
			{/if}
		{/if}
	</Modal>
{/if}

{#if deleteUserTarget}
	<Modal
		title={t('accounts.deleteTitle')}
		onclose={closeDelete}
		busy={deleteBusy}
		onsubmit={submitDelete}
		tone="danger"
		submitLabel={t('accounts.deleteConfirm')}
		busyLabel={t('common.saving')}
	>
		<p class="text-sm text-muted">
			{t('accounts.deleteBody', {
				name: deleteUserTarget.nickname,
				n: deleteUserTarget.file_count + deleteUserTarget.trashed_count
			})}
		</p>
		<Alert class="text-xs">{t('accounts.deleteWarning')}</Alert>

		{#if deleteError}
			<Alert>{deleteError}</Alert>
		{/if}
	</Modal>
{/if}
