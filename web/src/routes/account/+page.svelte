<script lang="ts">
	import { api, type Invitation, type Me, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let meta: Meta | undefined = $state();
	let me: Me | undefined = $state();
	let invitations: Invitation[] = $state([]);
	let email = $state('');
	let sending = $state(false);
	let inviteError = $state('');
	let inviteDone = $state('');
	let listError = $state('');
	let confirmDelete = $state(false);
	let deleteError = $state('');

	const tz = $derived(meta?.time_zone ?? 'UTC');
	const labels: Record<Invitation['status'], string> = {
		pending: 'Waiting',
		expired: 'Expired',
		used: 'Joined',
		cancelled: 'Cancelled'
	};

	api.meta().then((m) => (meta = m));
	api.me().then((m) => (me = m));
	function load() {
		api
			.invitations()
			.then((r) => (invitations = r))
			.catch((e) => (listError = e.message));
	}
	load();

	async function invite(ev: SubmitEvent) {
		ev.preventDefault();
		inviteError = inviteDone = '';
		sending = true;
		try {
			const inv = await api.invite(email.trim());
			inviteDone = `Invitation sent to ${inv.email}. It works once and expires in 7 days.`;
			email = '';
			load();
		} catch (e) {
			inviteError = (e as Error).message;
		} finally {
			sending = false;
		}
	}

	async function cancel(inv: Invitation) {
		inviteError = inviteDone = '';
		try {
			await api.cancelInvitation(inv.id);
			inviteDone = `Invitation to ${inv.email} cancelled.`;
			load();
		} catch (e) {
			inviteError = (e as Error).message;
		}
	}

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
	<button class="secondary" type="button" onclick={() => api.signOut()}>Sign out</button>
</div>

<section aria-labelledby="invite-heading">
	<h2 id="invite-heading">Invite someone</h2>
	<form onsubmit={invite}>
		<label for="invite-email">Their email address</label>
		<div class="row">
			<input id="invite-email" type="email" autocomplete="off" required bind:value={email} aria-describedby="invite-help" />
			<button type="submit" disabled={sending}>{sending ? 'Sending…' : 'Send invitation'}</button>
		</div>
		<p id="invite-help" class="help">
			{#if meta?.allowed_email_domains?.length}Addresses at {meta.allowed_email_domains.join(' or ')} only.{/if}
			They'll get an email with a link that works once, for 7 days, and a code to confirm their address.
		</p>
	</form>
	{#if inviteError}<p class="error" role="alert">{inviteError}</p>{/if}
	{#if inviteDone}<p class="ok" role="status">{inviteDone}</p>{/if}
</section>

<section aria-labelledby="invitations-heading">
	<h2 id="invitations-heading">Your invitations</h2>
	{#if listError}<p class="error" role="alert">{listError}</p>{/if}
	{#if invitations.length === 0 && !listError}<p class="status">You haven't invited anyone yet.</p>{/if}
	<ul class="cards">
		{#each invitations as inv (inv.id)}
			<li>
				<span class="who">{inv.email}</span>
				<span class="state {inv.status}">{labels[inv.status]}</span>
				<span class="when">
					Sent <time datetime={isoAttr(inv.created_at)}>{dateTime(inv.created_at, tz)}</time>{#if inv.status === 'pending'}, expires
						<time datetime={isoAttr(inv.expires_at)}>{dateTime(inv.expires_at, tz)}</time>{/if}
				</span>
				{#if inv.status === 'pending'}
					<button class="secondary" type="button" onclick={() => cancel(inv)} aria-label="Cancel: invitation to {inv.email}">Cancel</button>
				{/if}
			</li>
		{/each}
	</ul>
</section>

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
		gap: 0.25rem 1rem;
		margin: 0 0.75rem;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	section {
		margin: 1.5rem 0.75rem 0;
		max-width: 40rem;
	}
	h2 {
		font-size: 1.05rem;
		margin: 0 0 0.5rem;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: 0.75rem;
	}
	section .actions {
		margin: 0.5rem 0;
	}
	.row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.row input {
		flex: 1 1 14rem;
	}
	.help {
		color: var(--muted);
		font-size: 0.9rem;
		margin: 0.4rem 0;
	}
	section .error,
	section .ok,
	section .status {
		padding: 0.5rem 0;
	}
	section .ok {
		padding: 0.5rem 0.75rem;
	}
	.cards {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.cards li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.25rem 0.75rem;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--border);
	}
	.who {
		font-weight: 600;
		overflow-wrap: anywhere;
	}
	.when {
		color: var(--muted);
		font-size: 0.9rem;
		flex-basis: 100%;
	}
	.state {
		font-size: 0.85rem;
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0.5rem;
	}
	.cards button {
		margin-left: auto;
	}
</style>
