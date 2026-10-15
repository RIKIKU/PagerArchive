<script lang="ts">
	import { api, type ClientStatus, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let meta: Meta | undefined = $state();
	let rows: ClientStatus[] = $state([]);
	let error = $state('');

	api.meta().then((m) => (meta = m));
	const load = () =>
		api
			.clients()
			.then((r) => {
				rows = r;
				error = '';
			})
			.catch((e) => (error = e.message));
	// R39 is shown on screen: keep it current without a reload.
	$effect(() => {
		load();
		const t = setInterval(load, 30_000);
		return () => clearInterval(t);
	});

	function status(c: ClientStatus): string {
		if (!c.receiving) return 'Not receiving';
		if (c.clock_ahead) return 'Clock ahead';
		return 'Receiving';
	}
</script>

<svelte:head><title>Clients · PagerArchive</title></svelte:head>

<h1>Receiving clients</h1>
<p class="status">
	Admins only. A receiver is "not receiving" after 30 minutes with no page of any kind, heartbeats included. This
	page updates itself.
</p>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if rows.length}
	<table>
		<thead>
			<tr><th scope="col">Client</th><th scope="col">Status</th><th scope="col">Last page</th></tr>
		</thead>
		<tbody>
			{#each rows as c (c.id)}
				<tr>
					<td>{c.name} <span class="id">({c.id})</span></td>
					<td class:warn={!c.receiving || c.clock_ahead} data-testid="status-{c.id}">{status(c)}</td>
					<td>
						{#if c.last_seen}<time datetime={isoAttr(c.last_seen)}>{dateTime(c.last_seen, meta?.time_zone ?? 'UTC')}</time>
						{:else}Never{/if}
					</td>
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
	}
	.id {
		color: var(--muted);
	}
	.warn {
		color: var(--warn-fg);
		font-weight: 600;
	}
</style>
