<script lang="ts">
	import { api, type Invitation, type Me, type Meta, type UserRow } from '#lib/api.ts';
	import { date, dateTime, isoAttr } from '#lib/format.ts';

	let meta: Meta | undefined = $state();
	let me: Me | undefined = $state();
	let users: UserRow[] = $state([]);
	let invitations: Invitation[] = $state([]);
	let error = $state('');
	let notice = $state('');
	// The deactivation waiting for confirmation, if any. A tree goes through
	// its preview instead (R42b).
	let confirming: number | undefined = $state();
	let busy = $state(false);

	const tz = $derived(meta?.time_zone ?? 'UTC');
	const pending = $derived(invitations.filter((i) => i.status === 'pending'));

	api.meta().then((m) => (meta = m));
	api.me().then((m) => (me = m));
	function load() {
		api
			.users()
			.then((r) => (users = r))
			.catch((e) => (error = e.message));
		api
			.allInvitations()
			.then((r) => (invitations = r))
			.catch((e) => (error = e.message));
	}
	load();

	const label = (u: UserRow) => (u.name ? `${u.name} (${u.email})` : u.email);

	// run does an action and shows what it did.
	async function run(what: () => Promise<string>) {
		error = notice = '';
		busy = true;
		try {
			notice = await what();
			confirming = undefined;
			load();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	const deactivate = (u: UserRow) =>
		run(async () => {
			await api.deactivate(u.id);
			return `${label(u)} deactivated.`;
		});
	const setRole = (u: UserRow, role: 'admin' | 'user') =>
		run(async () => {
			await api.setRole(u.id, role);
			return role === 'admin' ? `${label(u)} is now an admin.` : `${label(u)} is no longer an admin.`;
		});
	const reactivate = (u: UserRow) =>
		run(async () => {
			await api.reactivate(u.id);
			return `${label(u)} reactivated. People they invited stay deactivated.`;
		});
	const cancel = (inv: Invitation) =>
		run(async () => {
			await api.cancelInvitation(inv.id);
			return `Invitation to ${inv.email} cancelled.`;
		});
</script>

<svelte:head><title>Users · PagerArchive</title></svelte:head>

<h1>Users</h1>
<p class="status">
	Deactivating someone signs them out at once and cancels their unused invitations. Deactivating their invite
	tree first shows everyone they invited, and everyone those people invited, so you can choose who to spare. Only an
	admin can invite a deactivated person again.
</p>
<p class="invite"><a href="/account#invite">Invite someone</a> from your account page, like everyone else.</p>
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}

<ul class="cards">
	{#each users as u (u.id)}
		<li class:off={u.status === 'deactivated'}>
			<div class="head">
				<a class="who" href="/admin/users/{u.id}">{label(u)}</a>
				{#if u.role === 'admin'}<span class="tag">Admin</span>{/if}
				{#if u.status === 'deactivated'}<span class="tag warn">Deactivated</span>{/if}
				{#if u.trusted}<span class="tag">Trusted</span>{/if}
				{#if u.id === me?.id}<span class="tag">You</span>{/if}
				{#if u.integrations_failing > 0}<span class="tag warn" data-testid="failing"
						>{u.integrations_failing} integration{u.integrations_failing === 1 ? '' : 's'} paused for failing</span
					>{/if}
			</div>
			<div class="meta">
				Invited by {u.invited_by ?? 'nobody (first admin)'} · joined
				<time datetime={isoAttr(u.created_at)}>{date(u.created_at, tz)}</time>
				{#if u.deactivated_at}· deactivated <time datetime={isoAttr(u.deactivated_at)}>{date(u.deactivated_at, tz)}</time>{/if}
				· {u.integrations} integration{u.integrations === 1 ? '' : 's'}
			</div>
			{#if u.id !== me?.id}
				{#if confirming === u.id}
					<div class="actions" role="group" aria-label="Confirm">
						<span>Deactivate {label(u)}?</span>
						<button class="danger" type="button" disabled={busy} onclick={() => deactivate(u)}>Deactivate</button>
						<button class="secondary" type="button" onclick={() => (confirming = undefined)}>Don't</button>
					</div>
				{:else}
					<div class="actions">
						{#if u.status === 'active'}
							<button class="secondary" type="button" onclick={() => (confirming = u.id)} aria-label="Deactivate: {label(u)}">Deactivate</button>
							<a class="button secondary" href="/admin/users/{u.id}/deactivate-tree" aria-label="Deactivate with invite tree: {label(u)}">Deactivate with invite tree</a>
							{#if u.role === 'user'}
								<button class="secondary" type="button" disabled={busy} onclick={() => setRole(u, 'admin')} aria-label="Make admin: {label(u)}">Make admin</button>
							{:else}
								<button class="secondary" type="button" disabled={busy} onclick={() => setRole(u, 'user')} aria-label="Remove admin: {label(u)}">Remove admin</button>
							{/if}
						{:else}
							<button class="secondary" type="button" disabled={busy} onclick={() => reactivate(u)} aria-label="Reactivate: {label(u)}">Reactivate</button>
						{/if}
					</div>
				{/if}
			{/if}
		</li>
	{/each}
</ul>

<h2>Unused invitations</h2>
{#if pending.length === 0}<p class="status">None.</p>{/if}
<ul class="cards">
	{#each pending as inv (inv.id)}
		<li>
			<div class="head">
				<span class="who">{inv.email}</span>
				{#if inv.reinvite}<span class="tag">Re-invitation</span>{/if}
			</div>
			<div class="meta">
				From {inv.invited_by} · expires <time datetime={isoAttr(inv.expires_at)}>{dateTime(inv.expires_at, tz)}</time>
			</div>
			<div class="actions">
				<button class="secondary" type="button" disabled={busy} onclick={() => cancel(inv)} aria-label="Cancel: invitation to {inv.email}">Cancel</button>
			</div>
		</li>
	{/each}
</ul>

<style>
	h2 {
		font-size: var(--text-lg);
		margin: 1.5rem 0.75rem 0.25rem;
	}
	.ok {
		margin: 0 0.75rem;
	}
	.invite {
		margin: 0.5rem 0.75rem;
	}
	.cards {
		list-style: none;
		padding: 0;
		margin: 0 0.75rem;
	}
	.cards li {
		padding: 0.6rem 0;
		border-bottom: 1px solid var(--border);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.5rem;
		align-items: center;
	}
	.who {
		font-weight: 600;
		overflow-wrap: anywhere;
	}
	.off .who {
		color: var(--muted);
	}
	.tag {
		font-size: var(--text-xs);
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0.5rem;
	}
	.tag.warn {
		background: var(--warn-bg);
		color: var(--warn-fg);
		border-color: var(--warn-fg);
	}
	.meta {
		color: var(--muted);
		font-size: var(--text-md);
		overflow-wrap: anywhere;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		align-items: center;
		margin-top: 0.4rem;
	}
</style>
