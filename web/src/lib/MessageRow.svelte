<script lang="ts">
	import type { Message } from './api.ts';
	import { date, time, isoAttr } from './format.ts';
	import AgencyIcon from './AgencyIcon.svelte';
	import PriorityBadge from './PriorityBadge.svelte';
	import PrintTick from './PrintTick.svelte';
	import AlbumNote from './AlbumNote.svelte';

	// pick: a tick box for the print sheet, on the print page only (R67).
	// selected: the message open beside the list (R80); keepScroll: opening
	// it leaves the list where it is. album: the album listed, whose note on
	// this message the row shows and edits (R82f).
	let {
		m,
		tz,
		fresh = false,
		pick = false,
		selected = false,
		keepScroll = false,
		album,
		onremoved
	}: {
		m: Message;
		tz: string;
		fresh?: boolean;
		pick?: boolean;
		selected?: boolean;
		keepScroll?: boolean;
		album?: number;
		onremoved?: (id: number) => void;
	} = $props();

	// R35d: the alias opens the alias view; a capcode with no alias opens its own.
	const whoHref = $derived(m.alias_id != null ? `/aliases/${m.alias_id}` : `/capcodes/${m.capcode}`);
	const priority = $derived(
		m.priority === 'Emergency' ? 'emergency' : m.priority === 'Non-emergency' ? 'nonemergency' : m.priority === 'Admin' ? 'admin' : 'plain'
	);
</script>

<!-- R35d: the whole row opens the detail view (the text's link stretches over
     it); the alias and job links sit above it. R31: priority, date and time,
     the template's icon and the alias (R66), group and job, then the text. -->
<article class="row {priority}" class:fresh class:selected aria-labelledby="m{m.id}-who" data-message-id={m.id}>
	<header>
		<PriorityBadge priority={m.priority} heartbeat={m.heartbeat} />
		<time datetime={isoAttr(m.sent_at)}>
			<span class="date">{date(m.sent_at, tz)}</span>
			<span class="time">{time(m.sent_at, tz)}</span>
		</time>
		<span class="who-wrap"
			>{#if m.icon}<AgencyIcon src={m.icon} alt={m.agency ?? ''} />{/if}<a
				id="m{m.id}-who"
				class="who"
				href={whoHref}
				style:--agency={m.colour ?? 'transparent'}>{m.alias ?? m.capcode}</a
			></span
		>
		{#if m.group}<span class="group">{m.group}</span>{/if}
		{#if m.job_number}<a class="job" href="/jobs/{encodeURIComponent(m.job_number)}">Job {m.job_number}</a>{/if}
		{#if pick}<PrintTick id={m.id} />{/if}
	</header>
	<p class="text">
		<a class="open" href="/messages/{m.id}" aria-current={selected ? 'true' : undefined} data-sveltekit-reset={keepScroll ? 'false' : undefined}
			>{m.display_text || '(no text)'}</a
		>
	</p>
	{#if album !== undefined}<AlbumNote {m} {album} {onremoved} />{/if}
	{#if m.flags?.parse_failed || m.flags?.address_unsplit || m.flags?.location_mismatch}
		<footer>
			{#if m.flags?.parse_failed}<span class="flag">parse failed</span>{/if}
			{#if m.flags?.address_unsplit}<span class="flag">address unsplit</span>{/if}
			{#if m.flags?.location_mismatch}<span class="flag">location mismatch</span>{/if}
		</footer>
	{/if}
</article>

<style>
	/* How a row shows its priority is in the tokens (R82c). */
	.row {
		position: relative;
		min-width: 0;
		padding: var(--row-pad-y) var(--row-pad-end) var(--row-pad-y) var(--row-pad-x);
		border-bottom: var(--border-width) solid var(--border);
		border-left: var(--edge-width) solid transparent;
		background: var(--plain-row);
	}
	.row:has(.open:hover) {
		background: var(--plain-row-hover);
	}
	.row.emergency {
		background: var(--emergency-row);
		border-left-color: var(--emergency-edge);
	}
	.row.emergency:has(.open:hover) {
		background: var(--emergency-row-hover);
	}
	.row.nonemergency {
		background: var(--nonemergency-row);
		border-left-color: var(--nonemergency-edge);
	}
	.row.nonemergency:has(.open:hover) {
		background: var(--nonemergency-row-hover);
	}
	.row.admin {
		background: var(--admin-row);
		border-left-color: var(--admin-edge);
	}
	.row.admin:has(.open:hover) {
		background: var(--admin-row-hover);
	}
	.row.selected {
		box-shadow: inset 0 0 0 var(--border-width-strong) var(--accent);
	}
	.fresh {
		animation: fresh 3s ease-out;
	}
	@keyframes fresh {
		from {
			background: var(--highlight);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.fresh {
			animation: none;
			background: var(--highlight);
		}
	}
	header {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2xs) var(--space-md);
		font-size: var(--text-sm);
	}
	time {
		color: var(--muted);
		font-family: var(--font-meta);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.who-wrap {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		min-width: 0;
	}
	.who,
	.job {
		position: relative;
		z-index: var(--layer-content);
	}
	.who {
		padding-left: var(--space-xs);
		border-left: var(--agency-bar) solid var(--agency);
		color: var(--fg);
		font-weight: var(--weight-strong);
		text-decoration: none;
		overflow-wrap: anywhere;
	}
	.who:hover,
	.who:focus-visible {
		text-decoration: underline;
	}
	.group {
		color: var(--muted);
		font-family: var(--font-mono);
	}
	.job {
		margin-left: auto;
		font-family: var(--font-meta);
	}
	.text {
		margin: var(--space-xs) 0 0;
		font-family: var(--font-page);
		font-size: var(--text-page);
		line-height: var(--leading);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.open {
		color: inherit;
		text-decoration: none;
	}
	.open::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	.open:focus-visible {
		outline: none;
	}
	.open:focus-visible::after {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: calc(-1 * var(--focus-width));
	}
	footer {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-md);
		margin-top: var(--space-xs);
		font-size: var(--text-xs);
	}
	.flag {
		padding: 0 var(--space-2xs);
		border-radius: var(--radius-small);
		background: var(--warn-bg);
		color: var(--warn-fg);
	}
</style>
