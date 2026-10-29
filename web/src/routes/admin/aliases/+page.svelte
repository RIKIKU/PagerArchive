<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type AdminAliasRow } from '#lib/api.ts';
	import AliasForm from '#lib/AliasForm.svelte';

	// R53: every alias, searchable, with Add and Export (R58). ?add=<capcode>
	// opens Add for that capcode (from the capcode view).
	let q = $state('');
	let rows: AdminAliasRow[] | undefined = $state();
	let more = $state(false);
	let error = $state('');
	// What the alias page just did: a deletion, with counts (no names in URLs).
	const done = $derived.by(() => {
		const p = page.url.searchParams;
		if (!p.has('deleted')) return '';
		const n = (k: string) => Number(p.get(k)) || 0;
		const extra = [
			n('requests') ? `${n('requests')} waiting request${n('requests') === 1 ? '' : 's'} closed` : '',
			n('integrations') ? `${n('integrations')} integration${n('integrations') === 1 ? '' : 's'} updated` : ''
		].filter(Boolean);
		return `Alias deleted${extra.length ? `: ${extra.join(', ')}` : ''}.`;
	});
	let adding = $state(page.url.searchParams.has('add'));
	let timer: ReturnType<typeof setTimeout> | undefined;

	function load() {
		api
			.adminAliases(q.trim())
			.then((r) => ((rows = r.aliases), (more = r.more)))
			.catch((e) => (error = e.message));
	}
	load();

	function typed() {
		clearTimeout(timer);
		timer = setTimeout(load, 250);
	}

	async function add(a: Parameters<typeof api.createAlias>[0]) {
		const r = await api.createAlias(a);
		goto(`/admin/aliases/${r.id}?added=1`);
	}
</script>

<svelte:head><title>Aliases · PagerArchive</title></svelte:head>

<h1>Aliases</h1>
<p class="status">
	Changes here take effect at once. Users ask for changes in <a href="/admin/requests">Alias requests</a>. Templates are in
	<a href="/admin/templates">Templates</a>. Aliases are imported with the PagerArchive PowerShell module.
</p>
{#if done}<p class="ok" role="status">{done}</p>{/if}
<div class="tools">
	{#if !adding}<button type="button" onclick={() => (adding = true)}>Add alias</button>{/if}
	<a class="button secondary" href="/api/v1/admin/aliases/export" download data-testid="export">Export CSV</a>
</div>
{#if adding}
	<AliasForm
		heading="Add an alias"
		submitLabel="Add alias"
		initial={{ pattern: page.url.searchParams.get('add') ?? '', name: '', template_id: null, ignored: false }}
		onSave={add}
		onCancel={() => (adding = false)}
	/>
{/if}

<div class="search">
	<label for="alias-q">Find an alias</label>
	<input id="alias-q" type="search" bind:value={q} oninput={typed} placeholder="Name, capcode or pattern" autocomplete="off" />
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if rows}
	<p class="status" aria-live="polite">
		{rows.length === 0 ? 'No aliases match.' : `${rows.length}${more ? '+' : ''} alias${rows.length === 1 ? '' : 'es'}${more ? '; search to narrow them down' : ''}.`}
	</p>
	{#if rows.length}
		<div class="scroll">
			<table data-testid="alias-table">
				<thead>
					<tr><th scope="col">Name</th><th scope="col">Capcode or pattern</th><th scope="col">Template</th><th scope="col">Ignored</th></tr>
				</thead>
				<tbody>
					{#each rows as a (a.id)}
						<tr>
							<td>
								<a href="/admin/aliases/{a.id}">{a.name}</a>
								{#if a.waiting_requests}<span class="tag warn">{a.waiting_requests} waiting</span>{/if}
							</td>
							<td class="mono">{a.pattern}</td>
							<td>{a.template ?? 'None'}</td>
							<td>{a.ignored ? 'Yes' : 'No'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/if}

<style>
	.ok {
		margin: 0 0.75rem 0.5rem;
	}
	.tools {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: 0 0.75rem 0.5rem;
	}
	.search {
		margin: 0.75rem;
		max-width: 24rem;
	}
	.search label {
		display: block;
		margin-bottom: 0.2rem;
	}
	.scroll {
		margin: 0 0.75rem;
		overflow-x: auto;
	}
	table {
		border-collapse: collapse;
		width: 100%;
		font-size: var(--text-md);
	}
	th,
	td {
		text-align: left;
		padding: 0.35rem 0.75rem 0.35rem 0;
		border-bottom: 1px solid var(--border);
		vertical-align: top;
		overflow-wrap: anywhere;
	}
	th {
		color: var(--muted);
		font-weight: 600;
	}
	.mono {
		font-family: var(--mono);
		white-space: nowrap;
	}
	.tag {
		font-size: var(--text-xs);
		border-radius: 1rem;
		padding: 0 0.5rem;
		margin-left: 0.25rem;
		white-space: nowrap;
	}
	.tag.warn {
		background: var(--warn-bg);
		color: var(--warn-fg);
	}
</style>
