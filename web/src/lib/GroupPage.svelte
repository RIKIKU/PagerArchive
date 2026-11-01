<script lang="ts">
	// A group's start page (ADR 0015, decision 4): its pages, each with its
	// icon and what it's for, as Administration's start page has had since
	// R44b. A breadcrumb's group level links here.
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';
	import type { Entry, Group } from './nav.ts';

	let { group, testid, extra }: { group: Group; testid?: string; extra?: Snippet<[Entry]> } = $props();
</script>

<svelte:head><title>{group.label} · PagerArchive</title></svelte:head>

<h1>{group.label}</h1>
<ul class="cards" data-testid={testid}>
	{#each group.entries as e (e.href)}
		<li>
			<span class="icon-box"><Icon name={e.icon} /></span>
			<div>
				<a href={e.href}>{e.label}</a>
				{@render extra?.(e)}
				<p>{e.text}</p>
			</div>
		</li>
	{/each}
</ul>

<style>
	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(var(--field-wide), 1fr));
		gap: var(--space-md);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	li {
		position: relative;
		display: flex;
		align-items: flex-start;
		gap: var(--space-md);
		padding: var(--space-lg);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
	}
	li:hover {
		background: var(--hover);
	}
	.icon-box {
		display: inline-grid;
		place-items: center;
		flex: none;
		width: var(--target);
		height: var(--target);
		border-radius: var(--radius-button);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	a {
		font-size: var(--text-lg);
		font-weight: var(--weight-strong);
	}
	/* The whole card is the link's target. */
	a::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	p {
		margin: var(--space-2xs) 0 0;
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
