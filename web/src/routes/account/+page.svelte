<script lang="ts">
	// Profile: who you are, who invited you, and deleting your account.
	// Inviting someone has its own page (owner, 2026-11-02; ADR 0015).
	import { goto } from '$app/navigation';
	import { api, type Me, type Meta } from '#lib/api.ts';

	let meta: Meta | undefined = $state();
	let me: Me | undefined = $state();
	let confirmDelete = $state(false);
	let deleteError = $state('');

	// Links from before the invitations had a page of their own.
	if (location.hash === '#invite') goto('/account/invite', { replaceState: true });

	api.meta().then((m) => (meta = m));
	api.me().then((m) => (me = m));

	async function deleteAccount() {
		deleteError = '';
		try {
			await api.deleteMe();
			location.assign('/signin?signed_out=1');
		} catch (e) {
			deleteError = (e as Error).message;
		}
	}
</script>

<svelte:head><title>Your account · PagerArchive</title></svelte:head>

<h1>Your account</h1>
{#if me}
	<dl>
		<dt>Email</dt>
		<dd>{me.email}</dd>
		<dt>Name</dt>
		<dd>{me.name ?? '—'}</dd>
		<dt>Role</dt>
		<dd>{me.role === 'admin' ? 'Admin' : 'User'}</dd>
		<dt>Invited by</dt>
		<dd>{me.invited_by ?? 'Nobody: you set up PagerArchive'}</dd>
	</dl>
{/if}
<div class="actions">
	{#if meta?.account_url}<a class="button secondary" href={meta.account_url}>Change your name or password</a>{/if}
	<a class="button secondary" href="/account/invite">Invite someone</a>
	<button class="secondary" type="button" onclick={() => api.signOut()}>Sign out</button>
</div>

<section aria-labelledby="delete-heading">
	<h2 id="delete-heading">Delete your account</h2>
	<p class="help">
		You won't be able to sign in again. People you invited keep their accounts, shown as invited by "deleted
		user". Your unused invitations are cancelled. This can't be undone.
	</p>
	{#if !confirmDelete}
		<button class="danger" type="button" onclick={() => (confirmDelete = true)}>Delete my account…</button>
	{:else}
		<div class="actions">
			<button class="danger" type="button" onclick={deleteAccount}>Yes, delete my account</button>
			<button class="secondary" type="button" onclick={() => (confirmDelete = false)}>Keep my account</button>
		</div>
	{/if}
	{#if deleteError}<p class="error" role="alert">{deleteError}</p>{/if}
</section>

<style>
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--space-2xs) var(--space-lg);
		margin: 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	section {
		margin: var(--space-2xl) 0 0;
		max-width: var(--measure);
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 var(--space-sm);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin: var(--space-md) 0;
	}
	section .actions {
		margin: var(--space-sm) 0;
	}
	.help {
		color: var(--muted);
		font-size: var(--text-md);
		margin: var(--space-xs) 0;
	}
	.error {
		padding: var(--space-sm) 0;
	}
</style>
