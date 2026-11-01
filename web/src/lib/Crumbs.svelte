<script lang="ts">
	// R79: where the page sits in the menu's tree, the same however it was
	// reached. Each level above the current page is a link; Messages goes
	// back to the list as it was left (R81a).
	import type { Crumb } from './nav.ts';

	let { trail, listHref }: { trail: Crumb[]; listHref: string } = $props();
</script>

<nav class="crumbs" class:deep={trail.length > 1} aria-label="Breadcrumb">
	<ol>
		{#each trail as c, i (i)}
			<li>
				{#if c.href && i < trail.length - 1}<a href={c.href === '/' ? listHref : c.href}>{c.label}</a>
				{:else}<span aria-current={i === trail.length - 1 ? 'page' : undefined}>{c.label}</span>{/if}
			</li>
		{/each}
	</ol>
</nav>

<style>
	.crumbs {
		min-width: 0;
	}
	ol {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2xs) var(--space-sm);
		margin: 0;
		padding: 0;
		list-style: none;
		font-size: var(--text-sm);
	}
	li {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-width: 0;
		overflow-wrap: anywhere;
	}
	li + li::before {
		content: '/';
		color: var(--muted);
	}
	a {
		display: inline-flex;
		align-items: center;
		min-height: var(--target);
	}
	[aria-current] {
		font-weight: var(--weight-strong);
	}
	/* Phones: only below the top level, where it says something. */
	@media (max-width: 47.99rem) {
		.crumbs:not(.deep) {
			display: none;
		}
	}
	@media print {
		.crumbs {
			display: none;
		}
	}
</style>
