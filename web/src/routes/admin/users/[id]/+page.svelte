<script lang="ts">
	// R44b: one user's page in Administration, with their integrations: name,
	// type, status, last delivery, and Pause or Resume. Never where they send
	// or their secrets (R47); the API doesn't return them.
	import { page } from '$app/state';
	import { api, type AdminUser, type Meta } from '#lib/api.ts';
	import { date, dateTime, isoAttr } from '#lib/format.ts';
	import { kindLabels, statusLabel } from '#lib/integrations.ts';

	let meta: Meta | undefined = $state();
	let data: AdminUser | undefined = $state();
	let error = $state('');
	let notice = $state('');
	const id = $derived(page.params.id ?? '');
	const tz = $derived(meta?.time_zone ?? 'UTC');
	const label = $derived(data ? (data.user.name ? `${data.user.name} (${data.user.email})` : data.user.email) : 'User');

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.adminUser(id)
			.then((d) => (data = d))
			.catch((e) => (error = e.message));
	}
	$effect(load);

	async function act(fn: () => Promise<unknown>, done: string) {
		error = notice = '';
		try {
			await fn();
			notice = done;
			load();
		} catch (e) {
			error = (e as Error).message;
		}
	}
</script>

<svelte:head><title>{label} · Administration · PagerArchive</title></svelte:head>

<h1>{label}</h1>
{#if error}<p class="error" role="alert">{error}</p>{/if}
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if data}
	<dl>
		<dt>Role</dt>
		<dd>{data.user.role === 'admin' ? 'Admin' : 'User'}</dd>
		<dt>Status</dt>
		<dd>{data.user.status === 'active' ? 'Active' : 'Deactivated'}</dd>
		<dt>Invited by</dt>
		<dd>{data.user.invited_by ?? 'nobody (first admin)'}</dd>
		<dt>Joined</dt>
		<dd><time datetime={isoAttr(data.user.created_at)}>{date(data.user.created_at, tz)}</time></dd>
	</dl>
	<p class="note"><a href="/admin/users">Users</a> has deactivation and roles.</p>

	<section aria-labelledby="integrations-heading">
		<h2 id="integrations-heading">Integrations</h2>
		<p class="note">You can see that each integration exists and pause it. What it sends and where are its owner's.</p>
		{#if data.integrations.length === 0}
			<p class="status">No integrations.</p>
		{:else}
			<ul class="cards">
				{#each data.integrations as i (i.id)}
					<li data-testid="admin-integration">
						<span class="name">{i.name}</span>
						<span class="muted">{kindLabels[i.kind]}</span>
						<span class="state" class:paused={i.status === 'paused'}>{statusLabel(i)}</span>
						<span class="muted when"
							>Last delivered: {#if i.last_delivered_at}<time datetime={isoAttr(i.last_delivered_at)}>{dateTime(i.last_delivered_at, tz)}</time
								>{:else}never{/if}</span
						>
						{#if i.status === 'active'}
							<button
								type="button"
								class="secondary"
								onclick={() => act(() => api.adminPauseIntegration(i.id), `Paused “${i.name}”. Only an admin can resume it.`)}
								aria-label="Pause: {i.name}">Pause</button
							>
						{:else}
							<button type="button" class="secondary" onclick={() => act(() => api.adminResumeIntegration(i.id), `Resumed “${i.name}”.`)}
								aria-label="Resume: {i.name}">Resume</button
							>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{/if}

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
	.note {
		margin: 0.5rem 0.75rem;
		color: var(--muted);
		max-width: 44rem;
	}
	.ok,
	.error {
		margin: 0 0.75rem 0.5rem;
	}
	section {
		margin: 1rem 0;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0.75rem 0.25rem;
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
		padding: 0.5rem 0.75rem;
		border-bottom: 1px solid var(--border);
	}
	.name {
		font-weight: 600;
		overflow-wrap: anywhere;
	}
	.muted {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.when {
		flex-basis: 100%;
	}
	.state {
		font-size: var(--text-sm);
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0.5rem;
	}
	.state.paused {
		background: var(--warn-bg);
		color: var(--warn-fg);
		border-color: var(--warn-bg);
	}
	.cards button {
		margin-left: auto;
	}
</style>
