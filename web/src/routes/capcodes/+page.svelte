<script lang="ts">
	import { api, type Meta, type UnaliasedCapcode } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let meta: Meta | undefined = $state();
	let rows: UnaliasedCapcode[] = $state([]);
	let error = $state('');
	let loaded = $state(false);

	api.meta().then((m) => (meta = m));
	api
		.unaliased()
		.then((r) => (rows = r))
		.catch((e) => (error = e.message))
		.finally(() => (loaded = true));
</script>

<svelte:head><title>Unknown capcodes · PagerArchive</title></svelte:head>

<h1>Capcodes with no alias</h1>
<p class="status">Pages to these capcodes are stored and shown as text only. Give them an alias to identify them.</p>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if loaded && rows.length === 0 && !error}<p class="status">Every capcode has an alias.</p>{/if}
{#if rows.length}
	<table>
		<thead>
			<tr><th scope="col">Capcode</th><th scope="col">Messages</th><th scope="col">Last seen</th></tr>
		</thead>
		<tbody>
			{#each rows as r (r.capcode)}
				<tr>
					<td><a href="/?capcode={r.capcode}&unaliased=true">{r.capcode}</a></td>
					<td>{r.messages}</td>
					<td><time datetime={isoAttr(r.last_seen)}>{dateTime(r.last_seen, meta?.time_zone ?? 'UTC')}</time></td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}

<style>
	table {
		border-collapse: collapse;
		width: calc(100% - 1.5rem);
		margin: 0 0.75rem;
	}
	th,
	td {
		text-align: left;
		padding: 0.4rem 0.5rem 0.4rem 0;
		border-bottom: 1px solid var(--border);
		font-variant-numeric: tabular-nums;
	}
</style>
