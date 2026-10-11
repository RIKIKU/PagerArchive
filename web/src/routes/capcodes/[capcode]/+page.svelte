<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type CapcodeView, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/filters.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
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
	</section>
	<MessageList {query} base={{ capcode: c.capcode, unaliased: 'true' }} level={2} />
{/if}

<style>
	.about {
		padding: 0 0.75rem 0.5rem;
		border-bottom: 1px solid var(--border);
	}
	h1 {
		margin: 0.75rem 0 0.5rem;
	}
	dl {
		display: grid;
		grid-template-columns: minmax(6rem, max-content) 1fr;
		gap: 0.3rem 1rem;
		margin: 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
	}
</style>
