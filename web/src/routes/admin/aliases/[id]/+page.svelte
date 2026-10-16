<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type AdminAlias, type AliasInput, type Meta } from '#lib/api.ts';
	import AliasForm from '#lib/AliasForm.svelte';
	import AliasHistory from '#lib/AliasHistory.svelte';
	import RequestCard from '#lib/RequestCard.svelte';
	import { refreshWaiting } from '#lib/waiting.svelte.ts';

	// R53: one alias in Administration: edit (at once, no review), delete
	// (R55: its waiting requests close), its waiting requests and its
	// history (R57).
	let meta: Meta | undefined = $state();
	let a: AdminAlias | undefined = $state();
	let error = $state('');
	let notice = $state(page.url.searchParams.has('added') ? 'Alias added.' : '');
	let confirming = $state(false);
	let busy = $state(false);
	let formKey = $state(0);
	const id = $derived(page.params.id ?? '');
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	function load() {
		return api
			.adminAlias(id)
			.then((r) => (a = r))
			.catch((e) => (error = e.message));
	}
	$effect(() => {
		error = '';
		load();
	});

	async function save(input: AliasInput) {
		await api.editAlias(a!.id, input);
		notice = 'Saved. The change shows everywhere at once.';
		await load();
		formKey++;
	}

	async function remove() {
		error = notice = '';
		busy = true;
		try {
			const d = await api.deleteAlias(a!.id);
			refreshWaiting();
			goto(`/admin/aliases?deleted=1&requests=${d.requests}&integrations=${d.integrations}`, { replaceState: true });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
			confirming = false;
		}
	}

	const range = $derived(a ? /[%_]/.test(a.pattern) : false);
</script>

<svelte:head><title>{a?.name ?? 'Alias'} · Aliases · PagerArchive</title></svelte:head>

<h1>{a ? `Alias “${a.name}”` : 'Alias'}</h1>
<p class="links"><a href="/admin/aliases">All aliases</a>{#if a} · <a href="/aliases/{a.id}">Its messages</a>{/if}</p>
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if a}
	{#key formKey}
		<AliasForm
			heading="Edit"
			submitLabel="Save"
			initial={{ pattern: a.pattern, name: a.name, template_id: a.template_id, ignored: a.ignored }}
			onSave={save}
		/>
	{/key}

	<section class="danger-zone" aria-labelledby="delete-h">
		<h2 id="delete-h">Delete</h2>
		{#if confirming}
			<p data-testid="delete-confirm">
				Delete “{a.name}” ({a.pattern})? Its pages stay and show the capcode, or another alias that covers it.
				{#if a.requests.length}{a.requests.length} waiting request{a.requests.length === 1 ? '' : 's'} will be closed as “alias deleted”.{/if}
				{#if a.integrations}
					{a.integrations} integration{a.integrations === 1 ? ' selects' : 's select'} it;
					{range ? 'it will be removed from their selection.' : 'they will select its capcode instead.'}
				{/if}
			</p>
			<div class="actions">
				<button type="button" class="danger" disabled={busy} onclick={remove}>Delete alias</button>
				<button type="button" class="secondary" onclick={() => (confirming = false)}>Don't</button>
			</div>
		{:else}
			<button type="button" class="secondary" onclick={() => ((confirming = true), (notice = ''))}>Delete…</button>
		{/if}
	</section>

	{#if a.requests.length}
		<section class="requests" aria-labelledby="req-h">
			<h2 id="req-h">Waiting requests</h2>
			{#each a.requests as r (r.id)}
				<RequestCard {r} {tz} level={3}>
					{#snippet actions()}<a href="/admin/requests#request-{r.id}">Review it</a>{/snippet}
				</RequestCard>
			{/each}
		</section>
	{/if}

	<AliasHistory history={a.history ?? []} {tz} />
{/if}

<style>
	.links {
		margin: 0 0.75rem;
		font-size: var(--text-sm);
	}
	.ok {
		margin: 0.5rem 0.75rem;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 1rem 0 0.25rem;
	}
	.danger-zone,
	.requests {
		margin: 0 0.75rem;
		max-width: 44rem;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
