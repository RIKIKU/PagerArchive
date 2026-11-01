<script lang="ts">
	import type { Message } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import Badge from './Badge.svelte';
	import Icon from './Icon.svelte';

	// R35c's detail view, in the pane beside the list at 1,280 px and wider
	// (R80) and on a page of its own below that.
	let { m, tz, close, alias, job, map }: { m: Message; tz: string; close: string; alias: string; job: string; map: string } = $props();

	let added = $state(false);
	let copied = $state(false);

	const fields: [keyof Message, string][] = [
		['job_number', 'Job number'],
		['category', 'Event category'],
		['event_type', 'Event type'],
		['description', 'Description'],
		['address', 'Address'],
		['locality', 'Locality'],
		['grid_ref', 'Grid reference'],
		['units', 'Units'],
		['group', 'Group']
	];
	const show = (v: unknown) => (Array.isArray(v) ? v.join(', ') : String(v));
	const placed = { address: 'From the address', street: 'Approximate: the street, not the house number', grid: 'From the grid reference' };
</script>

<article class="p-detail" aria-labelledby="pd-title">
	<div class="bar">
		<a class="p-btn ghost back" href={close}><Icon name="back" /><span>Messages</span></a>
		<a class="p-btn ghost close" href={close} aria-label="Close the message"><Icon name="close" /></a>
	</div>
	<header>
		{#if m.icon}<span class="p-tile large"><img src={m.icon} alt={m.agency ?? ''} width="64" height="64" /></span>{/if}
		<div>
			<h2 id="pd-title">{m.alias ?? m.capcode}</h2>
			<p class="meta">
				<Badge priority={m.priority} heartbeat={m.heartbeat} />
				<time datetime={isoAttr(m.sent_at)}>{dateTime(m.sent_at, tz)}</time>
				{#if m.agency}<span>{m.agency}</span>{/if}
			</p>
		</div>
	</header>

	<div class="body">
		<div class="lead">
			<!-- P2: the full message, always, before anything else. -->
			<p class="text">{m.display_text}</p>
			<div class="actions">
				<button class="p-btn primary" type="button" onclick={() => (added = !added)}
					><Icon name="albums" />{added ? 'Added to album' : 'Add to album'}</button
				>
				{#if m.location}<a class="p-btn secondary" href={map}><Icon name="map" />Show on map</a>{/if}
				<button class="p-btn secondary" type="button" onclick={() => (copied = true)}
					><Icon name="link" />{copied ? 'Link copied' : 'Copy link'}</button
				>
			</div>
		</div>

		<!-- R35c: a small map centred on it, below the text on phones. The
		     prototype draws a stand-in: the real one is the self-hosted map. -->
		{#if m.location}
			<figure class="map">
				<div class="tiles" class:ring={m.location.approximate}>
					<span class="pin"><Icon name="location" /></span>
				</div>
				<figcaption>
					{m.address ?? ''}{m.locality ? `, ${m.locality}` : ''}{m.location.approximate ? ' (approximate)' : ''}. The real view shows the self-hosted map
					here.
				</figcaption>
			</figure>
		{/if}

		<div class="rest">
			<h3>Details</h3>
			<dl>
				<dt>Alias</dt>
				<dd><a href={alias}>{m.alias ?? `None (capcode ${m.capcode})`}</a></dd>
				<dt>Capcode</dt>
				<dd class="mono">{m.capcode}</dd>
				{#each fields as [k, label] (k)}
					{#if m[k] != null}
						<dt>{label}</dt>
						<dd>
							{#if k === 'job_number'}<a href={job}>{m.job_number}</a>
							{:else}{show(m[k])}{#if m.labels[k]}<span class="label"> ({m.labels[k]})</span>{/if}{/if}
						</dd>
					{/if}
				{/each}
				<dt>Location</dt>
				<dd>{m.location ? placed[m.location.source] : 'Not placed on the map'}</dd>
			</dl>

			<h3>Received by</h3>
			<table>
				<thead><tr><th scope="col">Receiver</th><th scope="col">Received</th></tr></thead>
				<tbody>
					{#each m.receptions ?? [] as r (r.client)}
						<tr><td>{r.client}</td><td><time datetime={isoAttr(r.received_at)}>{dateTime(r.received_at, tz)}</time></td></tr>
					{/each}
				</tbody>
			</table>
		</div>
	</div>
</article>
