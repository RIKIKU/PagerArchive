<script lang="ts">
	import { api, type Invitation, type Me, type Meta, type SwitcherSettings, type UserRow } from '#lib/api.ts';
	import { date, dateTime, isoAttr } from '#lib/format.ts';
	import { resentNotice } from '#lib/invitations.ts';

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
	// Unused invitations that still count: waiting, or with a link that no
	// longer works and needs a resend (ADR 0021).
	const pending = $derived(invitations.filter((i) => i.status === 'pending' || i.status === 'link_lost'));

	// R77f: with RadioArchive set, who sees the app switcher: everyone, or the
	// members of an Authentik group (the one the radio's gate admits), whom
	// Show switcher and Hide switcher add and remove.
	let everyone = $state(false);
	let group = $state('');
	let groupProblem: SwitcherSettings['group_problem'] = $state();
	const combined = $derived(!!meta?.radioarchive_url);
	const other = $derived(meta?.radioarchive_name ?? 'Radio');
	api.meta().then((m) => {
		meta = m;
		if (m.radioarchive_url) loadSwitcher();
	});
	function loadSwitcher() {
		api
			.switcherSettings()
			.then((s) => {
				everyone = s.everyone;
				group = s.group ?? '';
				groupProblem = s.group_problem;
			})
			.catch((e) => (error = e.message));
	}
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

	const label = (u: UserRow) => (u.service_account ? `${u.name} (service account)` : u.name ? `${u.name} (${u.email})` : u.email);

	// run does an action and shows what it did.
	async function run(what: () => Promise<string>) {
		error = notice = '';
		busy = true;
		try {
			notice = await what();
			confirming = undefined;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
			// A resend whose email failed still changed the link (R43d).
			load();
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
	const setSees = (u: UserRow, sees: boolean) =>
		run(async () => {
			try {
				await api.setSeesSwitcher(u.id, sees);
			} finally {
				// A missing group found, or found again.
				loadSwitcher();
			}
			return sees
				? `${label(u)} sees the switcher to ${other}, and is in ${group}.`
				: `${label(u)} no longer sees the switcher to ${other}, and is out of ${group}.`;
		});
	const setEveryone = (on: boolean) =>
		run(async () => {
			try {
				everyone = (await api.setSwitcherSettings(on)).everyone;
			} catch (e) {
				everyone = !on;
				throw e;
			}
			return on ? `Everyone sees the switcher to ${other}.` : `Only the people in ${group} see the switcher to ${other}.`;
		});
	const cancel = (inv: Invitation) =>
		run(async () => {
			await api.cancelInvitation(inv.id);
			return `Invitation to ${inv.email} cancelled.`;
		});
	const resend = (inv: Invitation) =>
		run(async () => {
			await api.resendInvitation(inv.id);
			return resentNotice(inv);
		});
</script>

<svelte:head><title>Users · PagerArchive</title></svelte:head>

<h1>Users</h1>
<p class="status">
	Deactivating someone signs them out at once and cancels their unused invitations. Deactivating their invite
	tree first shows everyone they invited, and everyone those people invited, so you can choose who to spare. Only an
	admin can invite a deactivated person again.
</p>
<p class="invite"><a href="/account/invite">Invite someone</a> from your account, like everyone else.</p>
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}

{#if combined}
	<section class="switcher" aria-labelledby="switcher-heading">
		<h2 id="switcher-heading">Switcher to {other}</h2>
		<p class="status">
			The switcher in the top bar opens {other}. The people in Authentik's group <code>{group}</code> see it, unless it's
			shown to everyone. Show switcher and Hide switcher add someone to that group or take them out of it, and
			{other} goes by the same group to decide who it lets in.
		</p>
		{#if groupProblem === 'missing'}
			<p class="problem" data-testid="switcher-group-problem">
				Authentik has no group {group}, so nobody sees the switcher unless it's shown to everyone. PagerArchive's Authentik
				blueprint makes it: check that Authentik has applied it, then reload this page.
			</p>
		{:else if groupProblem === 'unreadable'}
			<p class="problem" data-testid="switcher-group-problem">
				Authentik couldn't be reached to read the group {group}, so who sees the switcher may be out of date. It's read
				again every 30 seconds.
			</p>
		{/if}
		<label class="check">
			<input type="checkbox" checked={everyone} disabled={busy} onchange={(e) => setEveryone(e.currentTarget.checked)} />
			Show the switcher to everyone
		</label>
	</section>
{/if}

<ul class="cards">
	{#each users as u (u.id)}
		<li class:off={u.status === 'deactivated'}>
			<div class="head">
				<a class="who" href="/admin/users/{u.id}">{label(u)}</a>
				{#if u.service_account}<span class="tag" data-testid="service-account">Service account</span>{/if}
				{#if u.role === 'admin'}<span class="tag">Admin</span>{/if}
				{#if u.status === 'deactivated'}<span class="tag warn">Deactivated</span>{/if}
				{#if u.trusted}<span class="tag">Trusted</span>{/if}
				{#if combined && u.sees_switcher}<span class="tag" data-testid="sees-switcher">Sees switcher</span>{/if}
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
							{#if u.service_account}
								<!-- R62a: made, revoked and re-keyed only on the server. -->
							{:else if u.role === 'user'}
								<button class="secondary" type="button" disabled={busy} onclick={() => setRole(u, 'admin')} aria-label="Make admin: {label(u)}">Make admin</button>
							{:else}
								<button class="secondary" type="button" disabled={busy} onclick={() => setRole(u, 'user')} aria-label="Remove admin: {label(u)}">Remove admin</button>
							{/if}
						{:else if !u.service_account}
							<button class="secondary" type="button" disabled={busy} onclick={() => reactivate(u)} aria-label="Reactivate: {label(u)}">Reactivate</button>
						{/if}
					</div>
				{/if}
			{/if}
			{#if combined && !u.service_account}
				<!-- R77f: yourself too, to try it before the others. -->
				<div class="actions">
					{#if u.sees_switcher}
						<button class="secondary" type="button" disabled={busy} onclick={() => setSees(u, false)} aria-label="Hide switcher: {label(u)}">Hide switcher</button>
					{:else}
						<button class="secondary" type="button" disabled={busy} onclick={() => setSees(u, true)} aria-label="Show switcher: {label(u)}">Show switcher</button>
					{/if}
				</div>
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
				{#if inv.status === 'link_lost'}<span class="tag">Link no longer works</span>{/if}
			</div>
			<div class="meta">
				From {inv.invited_by} · expires <time datetime={isoAttr(inv.expires_at)}>{dateTime(inv.expires_at, tz)}</time>
			</div>
			<div class="actions">
				<button class="secondary" type="button" disabled={busy} onclick={() => resend(inv)} aria-label="Resend: invitation to {inv.email}">Resend</button>
				<button class="secondary" type="button" disabled={busy} onclick={() => cancel(inv)} aria-label="Cancel: invitation to {inv.email}">Cancel</button>
			</div>
		</li>
	{/each}
</ul>

<style>
	h2 {
		font-size: var(--text-lg);
		margin: var(--space-2xl) 0 var(--space-2xs);
	}
	.ok {
		margin: 0;
	}
	.switcher {
		margin: var(--space-lg) 0;
	}
	.switcher h2 {
		margin-top: 0;
	}
	.problem {
		background: var(--warn-bg);
		color: var(--warn-fg);
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius);
	}
	.check {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		min-height: var(--target);
	}
	.invite {
		margin: var(--space-sm) 0;
	}
	.cards {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.cards li {
		padding: var(--space-sm) 0;
		border-bottom: var(--border-width) solid var(--border);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2xs) var(--space-sm);
		align-items: center;
	}
	.who {
		font-weight: var(--weight-strong);
		overflow-wrap: anywhere;
	}
	.off .who {
		color: var(--muted);
	}
	.tag {
		font-size: var(--text-xs);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-pill);
		padding: 0 var(--space-sm);
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
		gap: var(--space-xs);
		align-items: center;
		margin-top: var(--space-xs);
	}
</style>
