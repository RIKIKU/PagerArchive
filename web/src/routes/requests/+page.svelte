<script lang="ts">
	import { page } from '$app/state';
	import { api, type AliasRequest, type Meta } from '#lib/api.ts';
	import AliasRequestForm from '#lib/AliasRequestForm.svelte';
	import RequestCard from '#lib/RequestCard.svelte';

	// R56: the signed-in user's alias requests, their status and feedback,
	// with Edit and Withdraw while they wait; and a request for a new alias
	// (R54). ?pattern= starts one for that capcode (from the capcode view).
	let meta: Meta | undefined = $state();
	let requests: AliasRequest[] | undefined = $state();
	let error = $state('');
	let notice = $state('');
	let editing: number | undefined = $state();
	let adding = $state(page.url.searchParams.has('pattern'));
	let confirming: number | undefined = $state();
	const tz = $derived(meta?.time_zone ?? 'UTC');
	const waiting = $derived(requests?.filter((r) => r.status === 'waiting') ?? []);
	const closed = $derived(requests?.filter((r) => r.status !== 'waiting') ?? []);

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.myRequests()
			.then((r) => (requests = r))
			.catch((e) => (error = e.message));
	}
	load();

	function done(msg: string) {
		editing = undefined;
		adding = false;
		notice = msg;
		load();
	}

	async function withdraw(r: AliasRequest) {
		error = notice = '';
		try {
			await api.withdrawRequest(r.id);
			confirming = undefined;
			notice = 'Request withdrawn.';
			load();
		} catch (e) {
			error = (e as Error).message;
		}
	}
</script>

<svelte:head><title>My requests · PagerArchive</title></svelte:head>

<h1>My requests</h1>
<p class="status">
	Ask for a new alias, a change to an alias, or its deletion. An admin reviews each request. You can edit or
	withdraw a request while it waits. To ask for a change, open the alias (tap its name in the message list).
</p>
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}

<div class="box">
	{#if adding}
		<AliasRequestForm kind="add" pattern={page.url.searchParams.get('pattern') ?? ''} onDone={done} onCancel={() => (adding = false)} />
	{:else}
		<button type="button" onclick={() => ((adding = true), (notice = ''))}>Request a new alias</button>
	{/if}
</div>

<h2>Waiting</h2>
{#if requests && waiting.length === 0}<p class="status">None.</p>{/if}
<div class="list">
	{#each waiting as r (r.id)}
		{#if editing === r.id}
			<AliasRequestForm
				kind={r.kind}
				existing={r}
				alias={r.current && r.alias_id ? { id: r.alias_id, ...r.current } : undefined}
				onDone={done}
				onCancel={() => (editing = undefined)}
			/>
		{:else}
			<RequestCard {r} {tz} level={3}>
				{#snippet actions()}
					{#if confirming === r.id}
						<span>Withdraw this request?</span>
						<button type="button" class="danger" onclick={() => withdraw(r)}>Withdraw</button>
						<button type="button" class="secondary" onclick={() => (confirming = undefined)}>Don't</button>
					{:else}
						<button type="button" class="secondary" onclick={() => ((editing = r.id), (notice = ''))} aria-label="Edit: request for {r.pattern}">Edit</button>
						<button type="button" class="secondary" onclick={() => (confirming = r.id)} aria-label="Withdraw: request for {r.pattern}">Withdraw</button>
					{/if}
				{/snippet}
			</RequestCard>
		{/if}
	{/each}
</div>

<h2>Closed</h2>
{#if requests && closed.length === 0}<p class="status">None yet.</p>{/if}
<div class="list" data-testid="closed-requests">
	{#each closed as r (r.id)}
		<RequestCard {r} {tz} level={3} />
	{/each}
</div>

<style>
	h2 {
		font-size: var(--text-lg);
		margin: 1.25rem 0.75rem 0.25rem;
	}
	.ok {
		margin: 0 0.75rem;
	}
	.box,
	.list {
		margin: 0 0.75rem;
	}
</style>
