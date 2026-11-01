<script lang="ts">
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/search.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import MessageList from '#lib/MessageList.svelte';
	import { MAX_SHEET, clearSheet, drop, sheet, tick } from '#lib/sheet.svelte.ts';

	// R67: the print page. Its search is the search page's (R33: the same
	// form, API and paging), with a tick box on each result; ticking happens
	// nowhere else. The print sheet below it keeps messages in the order they
	// were added; R68: it prints as each message's text alone, as a pager
	// shows it (R31), each in a heavy border and never split across paper
	// pages. The messages come from the API as the signed-in user sees them;
	// storage holds IDs only.
	const query = $derived(criteria(page.url));
	let meta: Meta | undefined = $state();
	let fetched: Record<number, Message> = $state({});
	let gone = $state(0);
	let error = $state('');
	let loading = $state(false);
	const asking = new Set<number>();
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m)).catch(() => {});

	// Fetch whatever is on the sheet and not yet here (a tick in another tab
	// adds to it). Messages that no longer exist leave the sheet.
	$effect(() => {
		if (!sheet.ready) return;
		const want = sheet.ids.filter((id) => !(id in fetched) && !asking.has(id));
		if (!want.length) return;
		for (const id of want) asking.add(id);
		loading = true;
		api
			.messagesById(want)
			.then((r) => {
				const add: Record<number, Message> = {};
				for (const m of r.messages) add[m.id] = m;
				fetched = { ...fetched, ...add };
				if (r.missing.length) {
					gone += r.missing.length;
					drop(r.missing);
				}
			})
			.catch((e) => (error = (e as Error).message))
			.finally(() => {
				for (const id of want) asking.delete(id);
				loading = asking.size > 0;
			});
	});

	const items = $derived(sheet.ids.flatMap((id) => (id in fetched ? [fetched[id]] : [])));
	const count = $derived(sheet.ids.length);

	function clear() {
		if (confirm(`Remove all ${count} message${count === 1 ? '' : 's'} from the print sheet?`)) {
			clearSheet();
			gone = 0;
		}
	}
</script>

<svelte:head><title>Print sheet · PagerArchive</title></svelte:head>

<div class="screen">
	<h1>Print sheet</h1>
	<p class="status">Search for messages, then tick "Print" on the ones to add to your sheet below.</p>
	<div class="notice" aria-live="polite">
		{#if sheet.notice}<p class="ok" data-testid="sheet-full">{sheet.notice}</p>{/if}
	</div>
	<section class="listbox" aria-label="Search for messages to print" data-testid="sheet-search">
		<MessageList {query} level={2} title="Search results" pick scroll={false} />
	</section>
	<h2 class="sheet-heading">Your sheet</h2>
	<p class="status" data-testid="sheet-count">
		{#if !sheet.ready}Loading…
		{:else if count === 0}The print sheet is empty. Tick "Print" on messages in the search results above to add them.
		{:else}{count} message{count === 1 ? '' : 's'}, in the order you added them (at most {MAX_SHEET}). Printing shows only each message's text,
			as a pager shows it.{/if}
	</p>
	<div aria-live="polite">
		{#if gone}<p class="ok" data-testid="sheet-gone">
				{gone} message{gone === 1 ? ' was' : 's were'} no longer available and {gone === 1 ? 'has' : 'have'} been removed from the sheet.
			</p>{/if}
	</div>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if count > 0}
		<div class="actions">
			<button type="button" onclick={() => window.print()} disabled={loading}>Print</button>
			<button type="button" class="secondary" onclick={clear}>Clear</button>
			<!-- R68: the browser's own headers and footers are the print dialog's (ADR 0012). -->
			<p class="hint" data-testid="print-hint">
				In the print dialog, untick "Headers and footers" to leave out the browser's own date, title and address.
			</p>
		</div>
		<p class="note">Printed pages are outside PagerArchive's control: retention and deactivation don't reach paper.</p>
	{/if}
</div>

<ol class="sheet" data-testid="sheet">
	{#each items as m (m.id)}
		<li class="item" data-message-id={m.id}>
			<p class="about screen">
				<a href="/messages/{m.id}"><time datetime={isoAttr(m.sent_at)}>{dateTime(m.sent_at, tz)}</time>, {m.alias ?? m.capcode}</a>
			</p>
			<p class="text">{m.display_text || '(no text)'}</p>
			<button type="button" class="secondary screen" onclick={() => tick(m.id, false)}
				>Remove<span class="visually-hidden"> the message from {m.alias ?? m.capcode} at {dateTime(m.sent_at, tz)}</span></button
			>
		</li>
	{/each}
</ol>

<style>
	.notice p {
		margin: 0;
	}
	.listbox {
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		overflow: hidden;
	}
	/* Below the results, on every screen width (A60). */
	.sheet-heading {
		font-size: var(--text-xl);
		margin: var(--space-2xl) 0 0;
		padding: var(--space-lg) 0 0;
		border-top: var(--border-width) solid var(--border);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		padding: 0;
	}
	.hint {
		align-self: center;
		margin: 0;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.note {
		padding: 0;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.sheet {
		list-style: none;
		margin: 0;
		padding: 0 0 var(--space-lg);
	}
	.item {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: var(--space-2xs) var(--space-md);
		align-items: start;
		border: var(--border-width-strong) solid var(--fg);
		border-radius: var(--radius);
		padding: var(--space-md) var(--space-lg);
		background: var(--surface);
		margin-bottom: var(--space-md);
	}
	.about {
		grid-column: 1 / -1;
		margin: 0;
		font-size: var(--text-sm);
	}
	.about a {
		color: var(--muted);
	}
	/* As the message list shows it (R31). */
	.text {
		margin: 0;
		font-family: var(--font-page);
		font-size: var(--text-page);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		min-width: 0;
	}

	/* R68: the text alone, black on white whatever the theme, each message in
	   a heavy black border with space between, and never split across paper
	   pages. Its values are fixed by R68, the same in every look: the print tokens in app.css. The
	   header, navigation and footer are hidden by the layout. */
	@media print {
		:global(html),
		:global(body) {
			background: var(--print-bg);
			color: var(--print-fg);
		}
		.screen {
			display: none;
		}
		/* Blocks, not a grid: page breaks are only avoided reliably inside
		   block layout in every engine. */
		.sheet {
			display: block;
			padding: 0;
		}
		.item {
			display: block;
			color: var(--print-fg);
			background: var(--print-bg);
			border: var(--print-rule) solid var(--print-fg);
			border-radius: 0;
			padding: var(--print-pad);
			margin: 0 0 var(--print-gap);
			break-inside: avoid;
			page-break-inside: avoid;
		}
		.text {
			color: var(--print-fg);
			font-size: var(--print-text);
		}
	}
</style>
