<script lang="ts">
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/filters.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import Filters from '#lib/Filters.svelte';
	import MapView from '#lib/MapView.svelte';
	import PriorityBadge from '#lib/PriorityBadge.svelte';

	// R35: located messages, the last 24 hours unless the search gives dates,
	// following the active filters and live updates (section 14).
	let meta: Meta | undefined = $state();
	let messages: Message[] = $state([]);
	let truncated = $state(false);
	let error = $state('');
	let selected: number[] = $state([]);
	let mapView: MapView | undefined = $state();

	const query = $derived(criteria(page.url));
	const queryKey = $derived(query.toString());
	const focus = $derived(Number(page.url.searchParams.get('focus')) || null);
	const tz = $derived(meta?.time_zone ?? 'UTC');
	const ranged = $derived(query.has('from') || query.has('to'));
	const shown = $derived(selected.length ? messages.filter((m) => selected.includes(m.id)) : messages);

	api.meta().then((m) => (meta = m)).catch((e) => (error = e.message));

	async function load(q: URLSearchParams) {
		try {
			const p = await api.map(q);
			messages = p.messages;
			truncated = p.truncated;
			selected = [];
			mapView?.refit();
		} catch (e) {
			error = (e as Error).message;
		}
	}

	$effect(() => {
		const q = new URLSearchParams(queryKey);
		load(q);
		const es = new EventSource(`/api/v1/stream?${q}`);
		let opened = false;
		es.onopen = () => {
			if (opened) load(q);
			opened = true;
		};
		es.addEventListener('message', (ev) => {
			const m: Message = JSON.parse((ev as MessageEvent).data);
			if (m.location && !messages.some((x) => x.id === m.id)) messages = [m, ...messages];
		});
		return () => es.close();
	});

	function where(m: Message): string {
		switch (m.location?.source) {
			case 'address':
				return 'Address';
			case 'street':
				return 'Street (approximate)';
			case 'grid':
				return 'Grid reference';
		}
		return '';
	}
</script>

<svelte:head><title>Map · PagerArchive</title></svelte:head>

{#if meta}
	<Filters {query} {meta} />
{/if}
<div class="bar">
	<h1>Map</h1>
	<p class="status">
		{messages.length} located message{messages.length === 1 ? '' : 's'}{ranged ? '' : ' from the last 24 hours'}{truncated
			? ' (the newest only: narrow the search to see more)'
			: ''}.
		{#if meta && !meta.map_tiles}No background map is configured; points are shown without one.{/if}
	</p>
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}

{#if meta}
	<MapView bind:this={mapView} {messages} {meta} {focus} onselect={(ids) => (selected = ids)} />
{/if}

<section aria-labelledby="located-h" class="located">
	<div class="list-head">
		<h2 id="located-h">{selected.length ? `Selected on the map (${selected.length})` : 'Located messages'}</h2>
		{#if selected.length}<button type="button" onclick={() => (selected = [])}>Show all</button>{/if}
	</div>
	<ul data-testid="located-list">
		{#each shown as m (m.id)}
			<li>
				<a href="/messages/{m.id}">
					<span class="when"><time datetime={isoAttr(m.sent_at)}>{dateTime(m.sent_at, tz)}</time></span>
					<span class="who">{m.alias ?? m.capcode}</span>
					<PriorityBadge priority={m.priority} heartbeat={m.heartbeat} />
					<span class="text">{m.display_text}</span>
					<span class="where">{where(m)}</span>
				</a>
			</li>
		{/each}
	</ul>
	{#if messages.length === 0 && !error}<p class="status">No located messages{ranged ? ' match this search' : ' in the last 24 hours'}.</p>{/if}
</section>

<style>
	.bar {
		padding: 0.5rem 0.75rem 0;
	}
	h1 {
		font-size: var(--text-xl);
		margin: 0.25rem 0;
	}
	.bar .status {
		margin: 0 0 0.5rem;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.located {
		padding: 0 0.75rem 1rem;
	}
	.list-head {
		display: flex;
		align-items: center;
		gap: 1rem;
	}
	h2 {
		font-size: var(--text-base);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li a {
		display: flex;
		flex-wrap: wrap;
		gap: 0.15rem 0.6rem;
		align-items: baseline;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--border);
		color: var(--fg);
		text-decoration: none;
	}
	li a:hover .text,
	li a:focus-visible .text {
		text-decoration: underline;
	}
	.when,
	.where {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.who {
		font-weight: 600;
	}
	.text {
		flex-basis: 100%;
		overflow-wrap: anywhere;
	}
</style>
