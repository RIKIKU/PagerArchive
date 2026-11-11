<script lang="ts">
	import { api, type Meta, type UnaliasedCapcode } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	// R78: the most messages first (all time), then the latest seen. Capcodes
	// with FEW messages or fewer (5) are one-offs, mostly noise: they're hidden
	// until asked for, and then listed too, in the same order.
	const FEW = 5;

	let meta: Meta | undefined = $state();
	let rows: UnaliasedCapcode[] = $state([]);
	let error = $state('');
	let loaded = $state(false);
	let all = $state(false);

	const hidden = $derived(rows.filter((r) => r.messages <= FEW).length);
	const shown = $derived(all ? rows : rows.filter((r) => r.messages > FEW));

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
{#if shown.length}
	<table>
		<thead>
			<tr><th scope="col">Capcode</th><th scope="col">Messages</th><th scope="col">First seen</th><th scope="col">Last seen</th></tr>
		</thead>
		<tbody>
			{#each shown as r (r.capcode)}
				<tr>
					<td><a href="/capcodes/{r.capcode}">{r.capcode}</a></td>
					<td>{r.messages}</td>
					<td><time datetime={isoAttr(r.first_seen)}>{dateTime(r.first_seen, meta?.time_zone ?? 'UTC')}</time></td>
					<td><time datetime={isoAttr(r.last_seen)}>{dateTime(r.last_seen, meta?.time_zone ?? 'UTC')}</time></td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}
{#if hidden && !all}
	<p class="more" data-testid="few">
		{shown.length ? `And ${hidden} more` : `${hidden} ${hidden === 1 ? 'capcode' : 'capcodes'}`} with {FEW} or fewer messages:
		<button type="button" class="secondary" onclick={() => (all = true)}>Show them</button>
	</p>
{/if}

<style>
	table {
		border-collapse: collapse;
		width: 100%;
		margin: 0;
	}
	th,
	td {
		text-align: left;
		padding: var(--space-xs) var(--space-sm) var(--space-xs) 0;
		border-bottom: var(--border-width) solid var(--border);
		font-variant-numeric: tabular-nums;
	}
	.more {
		margin: var(--space-md) 0 0;
	}
</style>
