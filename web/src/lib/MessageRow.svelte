<script lang="ts">
	import type { Message } from './api.ts';
	import { date, time, isoAttr } from './format.ts';
	import AgencyIcon from './AgencyIcon.svelte';
	import PriorityBadge from './PriorityBadge.svelte';

	let { m, tz, fresh = false }: { m: Message; tz: string; fresh?: boolean } = $props();

	// R35d: the alias opens the alias view; a capcode with no alias opens its own.
	const whoHref = $derived(m.alias_id != null ? `/aliases/${m.alias_id}` : `/capcodes/${m.capcode}`);
</script>

<!-- R35d: the whole row opens the detail view (the text's link stretches over
     it); the alias and job links sit above it. -->
<article class="row" class:fresh aria-labelledby="m{m.id}-who" data-message-id={m.id}>
	<header>
		<time datetime={isoAttr(m.sent_at)}>
			<span class="date">{date(m.sent_at, tz)}</span>
			<span class="time">{time(m.sent_at, tz)}</span>
		</time>
		<PriorityBadge priority={m.priority} heartbeat={m.heartbeat} />
		{#if m.icon}<AgencyIcon src={m.icon} alt={m.agency ?? ''} />{/if}
		<a id="m{m.id}-who" class="who" href={whoHref} style:--agency={m.colour ?? 'transparent'}>{m.alias ?? m.capcode}</a>
		{#if m.group}<span class="group">{m.group}</span>{/if}
	</header>
	<p class="text"><a class="open" href="/messages/{m.id}">{m.display_text || '(no text)'}</a></p>
	{#if m.job_number || m.flags?.parse_failed || m.flags?.address_unsplit || m.flags?.location_mismatch}
		<footer>
			{#if m.job_number}<a class="job" href="/jobs/{encodeURIComponent(m.job_number)}">Job {m.job_number}</a>{/if}
			{#if m.flags?.parse_failed}<span class="flag">parse failed</span>{/if}
			{#if m.flags?.address_unsplit}<span class="flag">address unsplit</span>{/if}
			{#if m.flags?.location_mismatch}<span class="flag">location mismatch</span>{/if}
		</footer>
	{/if}
</article>

<style>
	.row {
		position: relative;
		padding: 0.6rem 0.75rem;
		border-bottom: 1px solid var(--border);
		min-width: 0;
	}
	.row:has(.open:hover) {
		background: var(--surface);
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
		align-items: baseline;
		gap: 0.25rem 0.5rem;
		font-size: var(--text-sm);
	}
	time {
		color: var(--muted);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.who,
	.job {
		position: relative;
		z-index: var(--layer-content);
	}
	.who {
		font-weight: 600;
		color: var(--fg);
		text-decoration: none;
		border-left: 0.3rem solid var(--agency);
		padding-left: 0.3rem;
		overflow-wrap: anywhere;
	}
	.who:hover,
	.who:focus-visible {
		text-decoration: underline;
	}
	.group {
		color: var(--muted);
		font-family: var(--mono);
	}
	.text {
		margin: 0.25rem 0 0;
		font-family: var(--mono);
		font-size: var(--text-md);
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
		outline: 2px solid var(--accent);
		outline-offset: -2px;
	}
	footer {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		margin-top: 0.25rem;
		font-size: var(--text-xs);
	}
	.flag {
		color: var(--warn-fg);
		background: var(--warn-bg);
		padding: 0 0.3rem;
		border-radius: 0.2rem;
	}
</style>
