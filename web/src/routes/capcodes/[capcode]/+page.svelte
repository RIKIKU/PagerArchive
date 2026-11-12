<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type CapcodeView, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/search.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import AliasHistory from '#lib/AliasHistory.svelte';
	import MessageList from '#lib/MessageList.svelte';

	// R35e for a capcode with no alias: its count, first and last seen (as in
	// R33), then its messages. If an alias now covers it, show the alias view.
	let meta: Meta | undefined = $state();
	let c: CapcodeView | undefined = $state();
	let error = $state('');
	const capcode = $derived(page.params.capcode ?? '');
	const query = $derived(criteria(page.url));
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	$effect(() => {
		error = '';
		api
			.capcode(capcode)
			.then((x) => {
				if (x.alias_id != null) goto(`/aliases/${x.alias_id}${page.url.search}`, { replaceState: true });
				else c = x;
			})
			.catch((e) => (error = e.message));
	});
</script>

<svelte:head><title>Capcode {capcode} · PagerArchive</title></svelte:head>

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if c}
	<section class="about" aria-labelledby="capcode-h">
		<h1 id="capcode-h">Capcode {c.capcode}</h1>
		<dl>
			<dt>Alias</dt>
			<dd>None. Pages to it are stored and shown as text only.</dd>
			<dt>Messages</dt>
			<dd>{c.messages}</dd>
			{#if c.first_seen}<dt>First seen</dt>
				<dd><time datetime={isoAttr(c.first_seen)}>{dateTime(c.first_seen, tz)}</time></dd>{/if}
			{#if c.last_seen}<dt>Last seen</dt>
				<dd><time datetime={isoAttr(c.last_seen)}>{dateTime(c.last_seen, tz)}</time></dd>{/if}
		</dl>
		<p><a href="/reports/capcode?capcode={c.capcode}">Report for this capcode</a></p>
		{#if meta?.role === 'admin'}
			<p><a class="button secondary" href="/admin/aliases?add={c.capcode}">Add an alias for this capcode</a></p>
		{:else if meta}
			<p><a class="button secondary" href="/requests?pattern={c.capcode}">Request an alias for this capcode</a></p>
		{/if}
	</section>
	{#if meta?.role === 'admin' && c.history?.length}
		<AliasHistory history={c.history} {tz} />
	{/if}
	<div class="listbox"><MessageList {query} base={{ capcode: c.capcode, unaliased: 'true' }} level={2} /></div>
{/if}

<style>
	.about {
		max-width: var(--measure);
	}
	h1 {
		margin: 0 0 var(--space-md);
	}
	.listbox {
		margin-top: var(--space-xl);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		overflow: clip;
	}
	dl {
		display: grid;
		grid-template-columns: minmax(var(--label-min), max-content) 1fr;
		gap: var(--space-2xs) var(--space-lg);
		margin: 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
	}
</style>
