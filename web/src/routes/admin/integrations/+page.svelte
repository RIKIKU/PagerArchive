<script lang="ts">
	// R47, R51: admins see that integrations exist and can pause them, never
	// what they send or where; and they keep the list of private hosts and
	// ranges integrations may reach.
	import { api, type AdminIntegration, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import { kindLabels, statusLabel } from '#lib/integrations.ts';

	let meta: Meta | undefined = $state();
	let list: AdminIntegration[] = $state([]);
	let entries = $state('');
	let error = $state('');
	let notice = $state('');
	let saving = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.adminIntegrations()
			.then((l) => (list = l))
			.catch((e) => (error = e.message));
	}
	load();
	api
		.allowList()
		.then((a) => (entries = a.entries.join('\n')))
		.catch((e) => (error = e.message));

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

	async function saveAllowList(ev: SubmitEvent) {
		ev.preventDefault();
		error = notice = '';
		saving = true;
		try {
			const a = await api.setAllowList(entries.split(/[\s,]+/).filter(Boolean));
			entries = a.entries.join('\n');
			notice = a.entries.length ? `Allowed: ${a.entries.join(', ')}.` : 'No private addresses are allowed.';
		} catch (e) {
			error = (e as Error).message;
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head><title>All integrations · PagerArchive</title></svelte:head>

<h1>All integrations</h1>
<p class="note">Admins only. You can see that each integration exists and pause it. What it sends and where are its owner's.</p>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if notice}<p class="ok" role="status">{notice}</p>{/if}

<section aria-labelledby="list-heading">
	<h2 id="list-heading">Integrations</h2>
	{#if list.length === 0}
		<p class="status">Nobody has an integration yet.</p>
	{:else}
		<ul class="cards">
			{#each list as i (i.id)}
				<li data-testid="admin-integration">
					<span class="name">{i.name}</span>
					<span class="muted">{kindLabels[i.kind]} · {i.owner}{i.owner_status !== 'active' ? ` (${i.owner_status})` : ''}</span>
					<span class="state" class:paused={i.status === 'paused'}>{statusLabel(i)}</span>
					<span class="muted when"
						>Last delivered: {#if i.last_delivered_at}<time datetime={isoAttr(i.last_delivered_at)}>{dateTime(i.last_delivered_at, tz)}</time>{:else}never{/if}</span
					>
					{#if i.status === 'active'}
						<button type="button" class="secondary" onclick={() => act(() => api.adminPauseIntegration(i.id), `Paused “${i.name}”. Only an admin can resume it.`)}
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

<section aria-labelledby="hosts-heading">
	<h2 id="hosts-heading">Private addresses integrations may reach</h2>
	<p class="note">
		Integrations can't reach private, loopback or link-local addresses unless they're listed here, for example your Home
		Assistant. One per line: an address (192.168.1.10), a range (192.168.1.0/24) or a host name (homeassistant.lan).
	</p>
	<form onsubmit={saveAllowList}>
		<label for="hosts" class="visually-hidden">Allowed hosts and ranges</label>
		<textarea id="hosts" rows="5" bind:value={entries} spellcheck="false"></textarea>
		<button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
	</form>
</section>

<style>
	.note {
		margin: 0 0.75rem 0.5rem;
		color: var(--muted);
		max-width: 44rem;
	}
	section {
		margin: 1rem 0;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0.75rem 0.5rem;
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
		overflow-wrap: anywhere;
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
	form {
		margin: 0 0.75rem;
		max-width: 30rem;
		display: grid;
		gap: 0.5rem;
		justify-items: start;
	}
	textarea {
		font-family: var(--mono);
		font-size: var(--text-sm);
		color: inherit;
		background: var(--bg);
		border: 1px solid var(--muted);
		border-radius: 0.3rem;
		padding: 0.35rem 0.5rem;
		width: 100%;
	}
</style>
