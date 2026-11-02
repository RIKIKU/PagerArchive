<script lang="ts">
	// R43a: inviting someone, and the invitations you've sent: a page of its
	// own in Account (owner, 2026-11-02; ADR 0015).
	import { api, type Invitation, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let meta: Meta | undefined = $state();
	let invitations: Invitation[] = $state([]);
	let email = $state('');
	let sending = $state(false);
	let inviteError = $state('');
	let inviteDone = $state('');
	let listError = $state('');

	const tz = $derived(meta?.time_zone ?? 'UTC');
	const labels: Record<Invitation['status'], string> = {
		pending: 'Waiting',
		expired: 'Expired',
		used: 'Joined',
		cancelled: 'Cancelled'
	};

	api.meta().then((m) => (meta = m));
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
</script>

<svelte:head><title>Invite someone · PagerArchive</title></svelte:head>

<h1>Invite someone</h1>
<section aria-label="Send an invitation">
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

<style>
	section {
		max-width: var(--measure);
	}
	section + section {
		margin-top: var(--space-2xl);
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 var(--space-sm);
	}
	.row {
		display: flex;
		gap: var(--space-sm);
		flex-wrap: wrap;
	}
	.row input {
		flex: 1 1 var(--field-wide);
	}
	.help {
		color: var(--muted);
		font-size: var(--text-md);
		margin: var(--space-xs) 0;
	}
	.error,
	.status {
		padding: var(--space-sm) 0;
	}
	.ok {
		padding: var(--space-sm) var(--space-md);
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
		gap: var(--space-2xs) var(--space-md);
		padding: var(--space-sm) 0;
		border-bottom: var(--border-width) solid var(--border);
	}
	.who {
		font-weight: var(--weight-strong);
		overflow-wrap: anywhere;
	}
	.when {
		color: var(--muted);
		font-size: var(--text-md);
		flex-basis: 100%;
	}
	.state {
		font-size: var(--text-sm);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-pill);
		padding: 0 var(--space-sm);
	}
	.cards button {
		margin-left: auto;
	}
</style>
