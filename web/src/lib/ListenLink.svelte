<script lang="ts">
	// R92g: one Listen link for all the radio channels a page (or a job) is
	// for, under its text (unchanged: P2), opening the radio beside
	// PagerArchive in a new tab, which listens to them all at once: the
	// dispatch channel of each area it's in first (R92f, from the network
	// profile's layers), then each fireground channel it names (R92a). Only
	// when RADIOARCHIVE_URL is set (meta's radioarchive_url, R92) and the
	// user sees the app switcher (R77f's soft launch: me's show_switcher).
	// One of the row's links (R35d): it sits above the row's stretched link.
	// A job's view passes its channels itself (dispatch, fireground), with
	// no message.
	import { api, type Message } from './api.ts';
	import Icon from './Icon.svelte';
	import { listen, listenChannels, listenHref, listenName } from './listen.ts';

	// spaced: room below, before what follows it (the detail view's Add to album).
	let {
		m,
		dispatch,
		fireground,
		spaced = false,
		testid = 'listen'
	}: { m?: Message; dispatch?: number[]; fireground?: number[]; spaced?: boolean; testid?: string } = $props();

	let radio: string | undefined = $state();
	Promise.all([api.meta(), api.me()])
		.then(([x, me]) => (radio = (me.show_switcher && x.radioarchive_url) || undefined))
		.catch(() => {});

	const l = $derived(
		listen(dispatch ?? (m?.areas ?? []).flatMap((a) => (a.dispatch_channel != null ? [a.dispatch_channel] : [])), fireground ?? m?.fireground_channels ?? [])
	);
</script>

{#if radio && l}
	<p class="listen" class:spaced>
		<a href={listenHref(radio, l)} target="_blank" rel="noopener" aria-label={listenName(l)} title={listenChannels(l)} data-testid={testid}
			><Icon name="fireground" /><span class="label">Listen</span><span class="which">{listenChannels(l)}</span></a
		>
	</p>
{/if}

<style>
	.listen {
		position: relative;
		z-index: var(--layer-content);
		width: fit-content;
		max-width: 100%;
		margin: var(--space-xs) 0 0;
	}
	.spaced {
		margin-bottom: var(--space-sm);
	}
	a {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		max-width: 100%;
		padding: var(--space-2xs) var(--space-sm);
		border: var(--border-width) solid var(--area-tag-border);
		border-radius: var(--radius-pill);
		background: var(--area-tag-bg);
		color: var(--fg);
		font-size: var(--text-sm);
		text-decoration: none;
		white-space: nowrap;
	}
	.label {
		font-weight: var(--weight-strong);
	}
	/* Which channels: quieter, and cut short rather than pushing the page sideways. */
	.which {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--muted);
		font-size: var(--text-xs);
	}
	a:hover .label {
		text-decoration: underline;
	}
	a:focus-visible {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: var(--focus-width);
	}
	/* Phones and fingers: at least the minimum tap target. */
	@media (max-width: 47.99rem), (pointer: coarse) {
		a {
			min-width: var(--target);
			min-height: var(--target);
			padding: 0 var(--space-md);
		}
	}
	/* Paper can't follow a link. */
	@media print {
		.listen {
			display: none;
		}
	}
</style>
