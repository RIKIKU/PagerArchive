<script lang="ts">
	// R92a: a chip for each fireground channel a page names, under its text
	// (unchanged: P2), linking to the radio beside PagerArchive in a new tab:
	// only when RADIOARCHIVE_URL is set (meta's radioarchive_url, R92). One
	// of the row's links (R35d): it sits above the row's stretched link. On a
	// phone the chips wrap onto their own line, each at least the minimum tap
	// target.
	import { api, type Message } from './api.ts';
	import Icon from './Icon.svelte';

	// spaced: room below, before what follows it (the detail view's Add to album).
	let { m, spaced = false }: { m: Message; spaced?: boolean } = $props();

	let radio: string | undefined = $state();
	api
		.meta()
		.then((x) => (radio = x.radioarchive_url || undefined))
		.catch(() => {});
</script>

{#if radio && m.fireground_channels?.length}
	<ul class="fireground" class:spaced aria-label="Fireground channels" data-testid="fireground">
		{#each m.fireground_channels as n (n)}
			<li>
				<a href="{radio}/?fireground={n}" target="_blank" rel="noopener" aria-label="Listen to fireground {n} (opens in a new tab)"
					><Icon name="fireground" /><span>Fireground {n}</span></a
				>
			</li>
		{/each}
	</ul>
{/if}

<style>
	.fireground {
		position: relative;
		z-index: var(--layer-content);
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs) var(--space-sm);
		width: fit-content;
		max-width: 100%;
		margin: var(--space-xs) 0 0;
		padding: 0;
		list-style: none;
	}
	.spaced {
		margin-bottom: var(--space-sm);
	}
	a {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2xs);
		padding: var(--space-2xs) var(--space-sm);
		border: var(--border-width) solid var(--area-tag-border);
		border-radius: var(--radius-pill);
		background: var(--area-tag-bg);
		color: var(--fg);
		font-size: var(--text-sm);
		font-weight: var(--weight-strong);
		text-decoration: none;
		white-space: nowrap;
	}
	a:hover {
		text-decoration: underline;
	}
	a:focus-visible {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: var(--focus-width);
	}
	/* Phones and fingers: each chip at least the minimum tap target. */
	@media (max-width: 47.99rem), (pointer: coarse) {
		a {
			min-width: var(--target);
			min-height: var(--target);
			padding: 0 var(--space-md);
		}
	}
	/* Paper can't follow a link. */
	@media print {
		.fireground {
			display: none;
		}
	}
</style>
