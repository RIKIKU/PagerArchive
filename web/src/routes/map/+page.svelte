<script lang="ts">
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import { rangeProblem } from '#lib/filters.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import AgencyIcon from '#lib/AgencyIcon.svelte';
	import Icon from '#lib/Icon.svelte';
	import MapView from '#lib/MapView.svelte';
	import PriorityBadge from '#lib/PriorityBadge.svelte';
	import { apiParams, criteria } from '#lib/search.ts';

	// R35: located messages, the last 24 hours unless the search gives dates,
	// following the search box (R33a) and live updates (section 14).
	let meta: Meta | undefined = $state();
	let messages: Message[] = $state([]);
	let truncated = $state(false);
	let error = $state('');
	let selected: number[] = $state([]);
	let mapView: MapView | undefined = $state();

	const query = $derived(criteria(page.url));
	const asked = $derived(meta ? apiParams(query, meta.categories) : undefined);
	const queryKey = $derived(asked?.params.toString());
	const askedProblem = $derived(asked?.problem ?? '');
	const focus = $derived(Number(page.url.searchParams.get('focus')) || null);
	const tz = $derived(meta?.time_zone ?? 'UTC');
	const ranged = $derived(query.has('from') || query.has('to'));
	const shown = $derived(selected.length ? messages.filter((m) => selected.includes(m.id)) : messages);
	const listHref = $derived(query.toString() ? `/?${query}` : '/');

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
		if (queryKey === undefined) return;
		const q = new URLSearchParams(queryKey);
		// A reversed range in the URL: say so, without asking the API.
		// error is only written here: reading it would make this effect depend on it.
		const problem = rangeProblem(q.get('from'), q.get('to')) || askedProblem;
		error = problem;
		if (problem) {
			messages = [];
			return;
		}
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

<div class="bar">
	<h1>Map</h1>
	<div class="views" role="group" aria-label="View">
		<a class="button secondary" href={listHref}><Icon name="list" />List</a>
		<span class="button secondary" aria-current="page"><Icon name="map" />Map</span>
	</div>
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
		{#if selected.length}<button class="secondary" type="button" onclick={() => (selected = [])}>Show all</button>{/if}
	</div>
	<ul data-testid="located-list">
		{#each shown as m (m.id)}
			<li>
				<a href="/messages/{m.id}">
					<PriorityBadge priority={m.priority} heartbeat={m.heartbeat} />
					<span class="when"><time datetime={isoAttr(m.sent_at)}>{dateTime(m.sent_at, tz)}</time></span>
					{#if m.icon}<AgencyIcon src={m.icon} alt={m.agency ?? ''} />{/if}
					<span class="who">{m.alias ?? m.capcode}</span>
					<span class="where">{where(m)}</span>
					<span class="text">{m.display_text}</span>
				</a>
			</li>
		{/each}
	</ul>
	{#if messages.length === 0 && !error}<p class="status">No located messages{ranged ? ' match this search' : ' in the last 24 hours'}.</p>{/if}
</section>

<style>
	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-sm) var(--space-lg);
		margin-bottom: var(--space-md);
	}
	h1 {
		margin: 0;
	}
	.views {
		display: flex;
		margin-left: auto;
	}
	.views > :first-child {
		border-top-right-radius: 0;
		border-bottom-right-radius: 0;
	}
	.views > * + * {
		margin-left: calc(-1 * var(--border-width));
		border-top-left-radius: 0;
		border-bottom-left-radius: 0;
	}
	.bar .status {
		flex-basis: 100%;
		margin: 0;
		padding: 0;
		font-size: var(--text-sm);
	}
	.located {
		margin-top: var(--space-lg);
	}
	.list-head {
		display: flex;
		align-items: center;
		gap: var(--space-lg);
	}
	h2 {
		font-size: var(--text-lg);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li a {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs) var(--space-md);
		align-items: center;
		padding: var(--space-md) 0;
		border-bottom: var(--border-width) solid var(--border);
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
		font-weight: var(--weight-strong);
	}
	.text {
		flex-basis: 100%;
		font-family: var(--font-page);
		overflow-wrap: anywhere;
	}
</style>
