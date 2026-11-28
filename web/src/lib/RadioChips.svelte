<script lang="ts">
	// R92a, R92f: a chip for each radio channel a page is for, under its text
	// (unchanged: P2), linking to the radio beside PagerArchive in a new tab:
	// first the dispatch channel of each area it's in (R92f, from the network
	// profile's layers), then each fireground channel it names (R92a). Only
	// when RADIOARCHIVE_URL is set (meta's radioarchive_url, R92) and the
	// user sees the app switcher (R77f's soft launch: me's show_switcher).
	// One of the row's links (R35d): it sits above the row's stretched link.
	// On a phone the chips wrap onto their own line, each at least the
	// minimum tap target. A job's view passes its areas' dispatch channels
	// alone (dispatch), with no message.
	import { api, type Message } from './api.ts';
	import Icon from './Icon.svelte';

	// spaced: room below, before what follows it (the detail view's Add to album).
	let { m, dispatch, spaced = false, testid = 'radio-channels' }: { m?: Message; dispatch?: number[]; spaced?: boolean; testid?: string } = $props();

	let radio: string | undefined = $state();
	Promise.all([api.meta(), api.me()])
		.then(([x, me]) => (radio = (me.show_switcher && x.radioarchive_url) || undefined))
		.catch(() => {});

	const unique = (ns: number[]) => ns.filter((n, i) => ns.indexOf(n) === i);
	const dispatchNumbers = $derived(unique(dispatch ?? (m?.areas ?? []).flatMap((a) => (a.dispatch_channel != null ? [a.dispatch_channel] : []))));
	const fireground = $derived(m?.fireground_channels ?? []);
</script>

{#if radio && (dispatchNumbers.length || fireground.length)}
	<ul class="channels" class:spaced aria-label="Radio channels" data-testid={testid}>
		{#each dispatchNumbers as n (n)}
			<li>
				<a href="{radio}/?fireground={n}" target="_blank" rel="noopener" data-kind="dispatch" aria-label="Listen to dispatch channel {n} (opens in a new tab)"
					><Icon name="fireground" /><span>Dispatch {n}</span></a
				>
			</li>
		{/each}
		{#each fireground as n (n)}
			<li>
				<a href="{radio}/?fireground={n}" target="_blank" rel="noopener" data-kind="fireground" aria-label="Listen to fireground {n} (opens in a new tab)"
					><Icon name="fireground" /><span>Fireground {n}</span></a
				>
			</li>
		{/each}
	</ul>
{/if}

<style>
	.channels {
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
		.channels {
			display: none;
		}
	}
</style>
