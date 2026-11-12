<script lang="ts">
	// R35c: a message's detail view: in a pane beside the list from 1,280 px
	// (R80), otherwise a page of its own. The full text comes first, then the
	// small map, then every field (P2).
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import { date, dateTime, isoAttr, isoDate, time } from '#lib/format.ts';
	import AddToAlbum from '#lib/AddToAlbum.svelte';
	import AgencyIcon from '#lib/AgencyIcon.svelte';
	import { setTitle } from '#lib/here.svelte.ts';
	import MapView from '#lib/MapView.svelte';
	import PriorityBadge from '#lib/PriorityBadge.svelte';
	import { screen } from '#lib/screen.svelte.ts';

	let meta: Meta | undefined = $state();
	let m: Message | undefined = $state();
	let error = $state('');
	const id = $derived(page.params.id ?? '');
	const tz = $derived(meta?.time_zone ?? 'UTC');
	// Beside the list, the list has the page's heading.
	const heading = $derived(screen.wide ? 'h2' : 'h1');

	api.meta().then((x) => (meta = x));
	$effect(() => {
		error = '';
		api
			.message(id)
			.then((x) => (m = x))
			.catch((e) => (error = e.message));
	});
	$effect(() => {
		if (m && meta) setTitle(`${m.alias ?? m.capcode}, ${date(m.sent_at, tz)} ${time(m.sent_at, tz)}`);
	});

	const standard: [keyof Message, string][] = [
		['job_number', 'Job number'],
		['event_type', 'Event type'],
		['description', 'Description'],
		['address', 'Address'],
		['locality', 'Locality'],
		['grid_ref', 'Grid reference'],
		// R8: the units field, shown as Appliances (owner, 2026-11-13).
		['units', 'Appliances'],
		['group', 'Group']
	];

	function show(v: unknown): string {
		if (v == null) return '—';
		if (Array.isArray(v)) return v.join(', ');
		return String(v);
	}

	function label(field: string, v: unknown): string {
		const l = m?.labels?.[field];
		return l ? `${show(v)} (${l})` : show(v);
	}

	const placed = {
		address: 'From the address',
		street: 'Approximate: the street, not the house number',
		grid: 'From the grid reference'
	};

	const humanise = (k: string) => k.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());
</script>

<svelte:head><title>Message · PagerArchive</title></svelte:head>

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if m && m.id === Number(id)}
	<div class="frame">
		<article class="detail" class:located={!!m.location}>
			<div class="lead">
				<header>
					{#if m.icon}<AgencyIcon src={m.icon} alt={m.agency ?? ''} large />{/if}
					<div>
						<svelte:element this={heading} class="title">{m.alias ?? m.capcode}</svelte:element>
						<p class="meta">
							<PriorityBadge priority={m.priority} heartbeat={m.heartbeat} />
							<time datetime={isoAttr(m.sent_at)}>{dateTime(m.sent_at, tz)}</time>
						</p>
					</div>
				</header>
				<!-- P2: the full message, always. -->
				<p class="text">{m.display_text}</p>
				<!-- R82: kept in the person's albums, from here only. -->
				<div class="keep"><AddToAlbum message={m.id} /></div>
			</div>
			<!-- R35c: where it was placed, after the text so the text stays first on phones (P2). -->
			{#if m.location && meta}
				<div class="where"><MapView messages={[m]} {meta} focus={m.id} variant="single" /></div>
			{/if}

			<div class="rest">
				<h2>Details</h2>
				<dl>
					<dt>Alias</dt>
					<dd>
						{#if m.alias_id != null}<a href="/aliases/{m.alias_id}">{m.alias}</a>
						{:else}<a href="/capcodes/{m.capcode}">None (capcode {m.capcode})</a>{/if}
					</dd>
					<dt>Capcode</dt>
					<dd>{m.capcode}</dd>
					{#if m.agency}<dt>Agency</dt>
						<dd>{m.agency}</dd>{/if}
					{#if m.category}<dt>Event category</dt>
						<dd>{m.category}</dd>{/if}
					{#each standard as [k, l] (k)}
						{#if m[k] != null}
							<dt>{l}</dt>
							<dd>
								{#if k === 'job_number'}<a href="/jobs/{encodeURIComponent(String(m[k]))}">{m[k]}</a>
								{:else}{label(k, m[k])}{/if}
							</dd>
						{/if}
					{/each}
					<dt>Location</dt>
					<dd data-testid="location">
						{#if m.location}
							{placed[m.location.source]}
							<a href="/map?focus={m.id}&from={isoDate(m.sent_at, tz)}&to={isoDate(m.sent_at, tz)}">Show on map</a>
						{:else}Not placed on the map{/if}
					</dd>
					{#each Object.entries(m.extra) as [k, v] (k)}
						<dt>{humanise(k)}</dt>
						<dd>{label(k, v)}</dd>
					{/each}
					<dt>Stored text</dt>
					<dd class="mono">{m.text}</dd>
				</dl>

				{#if m.flags}
					<h2>Flags <span class="note">(admins only)</span></h2>
					<ul class="flags">
						<li>Format: {m.format ?? 'none (text only or no match)'}</li>
						<li>Parse failed: {m.flags.parse_failed ? 'yes' : 'no'}</li>
						<li>Address unsplit: {m.flags.address_unsplit ? 'yes' : 'no'}</li>
						<li>Location mismatch: {m.flags.location_mismatch ? 'yes' : 'no'}</li>
					</ul>
				{/if}

				<h2>Received by</h2>
				<table>
					<thead><tr><th scope="col">Receiver</th><th scope="col">Received</th></tr></thead>
					<tbody>
						{#each m.receptions ?? [] as r, i (i)}
							<tr><td>{r.client}</td><td><time datetime={isoAttr(r.received_at)}>{dateTime(r.received_at, tz)}</time></td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</article>
	</div>
{/if}

<style>
	.frame {
		container-type: inline-size;
	}
	.detail {
		display: grid;
		grid-template-areas: 'lead' 'map' 'rest';
		gap: 0 var(--space-2xl);
	}
	/* Room for the map beside the text and details. */
	@container (min-width: 46rem) {
		.located {
			grid-template-columns: minmax(0, 1fr) var(--detail-map-width);
			grid-template-areas: 'lead map' 'rest map';
			align-items: start;
		}
	}
	.lead {
		grid-area: lead;
	}
	.where {
		grid-area: map;
		margin: 0 0 var(--space-lg);
	}
	.rest {
		grid-area: rest;
	}
	header {
		display: flex;
		align-items: center;
		gap: var(--space-lg);
		margin: 0 0 var(--space-lg);
	}
	.title {
		margin: 0;
		font-size: var(--text-2xl);
		line-height: var(--leading-tight);
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-xs) var(--space-md);
		margin: var(--space-xs) 0 0;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.text,
	.mono {
		font-family: var(--font-page);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.text {
		margin: 0 0 var(--space-lg);
		padding: var(--space-lg);
		background: var(--bg);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		font-size: var(--text-lg);
		line-height: var(--leading-loose);
	}
	.keep {
		margin: 0 0 var(--space-lg);
	}
	.rest h2 {
		margin: var(--space-xl) 0 var(--space-sm);
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.note {
		font-weight: var(--weight-normal);
	}
	dl {
		display: grid;
		grid-template-columns: minmax(var(--label-min), max-content) minmax(0, 1fr);
		gap: var(--space-xs) var(--space-lg);
		margin: 0;
		font-size: var(--text-sm);
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	.flags {
		margin: 0;
		padding-left: var(--space-xl);
		font-size: var(--text-sm);
	}
	table {
		width: 100%;
		font-size: var(--text-sm);
	}
	th,
	td {
		padding: var(--space-xs) var(--space-sm) var(--space-xs) 0;
		border-bottom: var(--border-width) solid var(--border);
		text-align: left;
	}
	th {
		color: var(--muted);
		font-weight: var(--weight-strong);
	}
</style>
