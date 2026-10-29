<script lang="ts">
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr, isoDate } from '#lib/format.ts';
	import AgencyIcon from '#lib/AgencyIcon.svelte';
	import MapView from '#lib/MapView.svelte';
	import PriorityBadge from '#lib/PriorityBadge.svelte';

	let meta: Meta | undefined = $state();
	let m: Message | undefined = $state();
	let error = $state('');
	const id = $derived(page.params.id ?? '');
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((x) => (meta = x));
	$effect(() => {
		api
			.message(id)
			.then((x) => (m = x))
			.catch((e) => (error = e.message));
	});

	const standard: [keyof Message, string][] = [
		['job_number', 'Job number'],
		['event_type', 'Event type'],
		['description', 'Description'],
		['address', 'Address'],
		['locality', 'Locality'],
		['grid_ref', 'Grid reference'],
		['units', 'Units'],
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
{#if m}
	<article class="detail" class:located={!!m.location}>
		<header>
			<div class="title">
				{#if m.icon}<AgencyIcon src={m.icon} alt={m.agency ?? ''} large />{/if}
				<h1>{m.alias ?? m.capcode}</h1>
			</div>
			<p class="meta">
				<time datetime={isoAttr(m.sent_at)}>{dateTime(m.sent_at, tz)}</time>
				<PriorityBadge priority={m.priority} heartbeat={m.heartbeat} />
			</p>
		</header>
		<!-- P2: the full message, always. -->
		<p class="text">{m.display_text}</p>
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
{/if}

<style>
	.detail {
		padding: 0 0.75rem 1rem;
	}
	.where {
		margin: 0.75rem 0 0;
	}
	/* Wider screens: the map beside the text and details. */
	@media (min-width: 48rem) {
		.located {
			display: grid;
			grid-template-columns: minmax(0, 1fr) 22rem;
			column-gap: 1.5rem;
			align-items: start;
		}
		.located header {
			grid-column: 1 / -1;
		}
		.located .text,
		.located .rest {
			grid-column: 1;
		}
		.located .where {
			grid-column: 2;
			grid-row: 2 / span 2;
			margin-top: 1rem;
		}
	}
	.title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin: 0.75rem 0 0.25rem;
	}
	.detail h1 {
		margin: 0;
	}
	h2 {
		font-size: var(--text-base);
		margin: 1.25rem 0 0.5rem;
	}
	.note {
		font-weight: 400;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.meta {
		display: flex;
		gap: 0.5rem;
		align-items: baseline;
		color: var(--muted);
		margin: 0;
	}
	.text,
	.mono {
		font-family: var(--mono);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.text {
		font-size: var(--text-base);
		background: var(--surface);
		padding: 0.75rem;
		border-radius: 0.3rem;
	}
	dl {
		display: grid;
		grid-template-columns: minmax(7rem, max-content) 1fr;
		gap: 0.3rem 1rem;
		margin: 0;
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
		padding-left: 1.2rem;
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	th,
	td {
		text-align: left;
		padding: 0.3rem 0.5rem 0.3rem 0;
		border-bottom: 1px solid var(--border);
	}
</style>
