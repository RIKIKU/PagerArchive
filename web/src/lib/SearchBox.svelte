<script lang="ts">
	// R33a: one search box, then Help, the dates and Clear all (M21). The box
	// is the suggestion control (Suggest.svelte, the ARIA combobox pattern):
	// clicking into the empty box (or between words) lists every key; typing
	// a key's value suggests its values, and typing a word with no key
	// suggests searching it as text, then the filters whose values contain it
	// (search.ts). Picking a value searches, as Enter does; picking a key puts
	// it in the box, ready for its value.
	// The search applies to the view it's on (the list, the map, an alias, a
	// job, the print page), and is kept in its address, Clear all included,
	// so Back restores it.
	import { goto } from '$app/navigation';
	import DateRange from './DateRange.svelte';
	import Icon from './Icon.svelte';
	import Suggest from './Suggest.svelte';
	import { boxText, examples, keysFor, parseBox, suggestions, type Values } from './search.ts';
	import { searchValues } from './searchValues.ts';

	let {
		action,
		query,
		admin,
		categories,
		timeZone,
		firstPage,
		lastPage
	}: { action: string; query: URLSearchParams; admin: boolean; categories: string[]; timeZone: string; firstPage?: string; lastPage?: string } = $props();

	const uid = $props.id();
	let text = $state('');
	let problem = $state('');
	let box: Suggest;

	// The box shows the search in the address, unless that's the search it
	// just sent: then what's been typed since stays.
	let sent: string | undefined;
	let shown: string | undefined;
	$effect(() => {
		const s = query.toString();
		if (s === shown) return;
		shown = s;
		if (s === sent) sent = undefined;
		else text = boxText(query);
	});
	const from = $derived(query.get('from') ?? '');
	const to = $derived(query.get('to') ?? '');
	const set = $derived(text.trim() !== '' || !!from || !!to);

	let values: Values = $state({ alias: [], agency: [], group: [], category: [] });
	let help = $state(false);
	let helpTipOff = $state(false);
	let clearTipOff = $state(false);
	let helpWrap: HTMLDivElement;

	function load() {
		searchValues()
			.then((v) => {
				values = { ...v, category: categories };
				box.refresh(); // the word being typed, again with them
			})
			.catch(() => {}); // suggestions without the API's values: keys and text
	}

	const suggest = (t: string, caret: number) => suggestions(t, caret, admin, { ...values, category: categories });

	/** Searches what's in the box, with these dates. */
	function run(dates = { from, to }) {
		box.close();
		const parsed = parseBox(text, admin);
		problem = parsed.problem;
		if (problem) return;
		const params = parsed.params;
		if (dates.from) params.set('from', dates.from);
		if (dates.to) params.set('to', dates.to);
		go(params);
	}

	function go(params: URLSearchParams) {
		const s = params.toString();
		if (s !== query.toString()) sent = s;
		// Focus stays where it is; the results start at the top.
		goto(s ? `${action}?${s}` : action, { reset: false }).then(() => window.scrollTo(0, 0));
	}

	function submit(ev: SubmitEvent) {
		ev.preventDefault();
		run();
	}

	/** Clear all: the box and the dates, a search like any other (so Back
	 *  brings them back); the focus goes to the box, as the control goes. */
	function clearAll() {
		text = '';
		problem = '';
		box.close();
		go(new URLSearchParams());
		box.focus(true);
	}

	function helpKey(e: KeyboardEvent) {
		if (e.key !== 'Escape') return;
		if (help) {
			help = false;
			helpWrap.querySelector('button')?.focus();
		} else helpTipOff = true;
	}

	function outside(e: PointerEvent) {
		if (help && !helpWrap.contains(e.target as Node)) help = false;
	}
</script>

<svelte:document onpointerdown={outside} />

<form class="search" role="search" aria-label="Search messages" onsubmit={submit}>
	<Suggest
		bind:this={box}
		bind:text
		{suggest}
		onpick={() => run()}
		onfocus={load}
		oninput={() => (problem = '')}
		label="Search"
		icon="search"
		placeholder={'Text, or alias:"Station 1", priority:emergency …'}
		listLabel="Search suggestions"
		describedby={problem ? `${uid}-problem` : undefined}
	/>
	<!-- svelte-ignore a11y_no_static_element_interactions (Escape closes the help, and hides the tooltip: WCAG 1.4.13) -->
	<div class="help" bind:this={helpWrap} onkeydown={helpKey}>
		<span class="has-tip" class:tip-off={helpTipOff || help} onpointerleave={() => (helpTipOff = false)} onfocusout={() => (helpTipOff = false)}>
		<button type="button" class="ghost icon-button" aria-label="Search help" aria-expanded={help} aria-controls="{uid}-help" onclick={() => (help = !help)}
			><Icon name="help" /></button
		>
		<span class="tooltip" aria-hidden="true">Search help</span>
		</span>
		<div class="panel" id="{uid}-help" hidden={!help}>
			<p>
				Type words to find pages containing every one of them; words in quotes, such as "check hoses", must be together, in that order.
				Filters narrow the search: a filter is a key and a value. A value with spaces goes in quotes; in quotes it must match exactly, and
				without them it matches any value containing it. The same key twice means either value.
			</p>
			<ul class="keys">
				{#each keysFor(admin) as k (k)}
					<li>
						<span class="key">{#if k === 'flag'}<span class="who">Flags (admins only)</span>{/if}<code>{examples[k].example}</code></span>
						<span class="what">{examples[k].what}</span>
					</li>
				{/each}
			</ul>
			<p>
				Keyboard: <kbd>Alt</kbd>+<kbd>J</kbd> (<kbd>Option</kbd>+<kbd>J</kbd> on a Mac) goes to the date scrubber beside a list of messages, and
				<kbd>Escape</kbd> comes back to the list.
			</p>
		</div>
	</div>
	<DateRange {from} {to} {timeZone} {firstPage} {lastPage} onchange={(f, t) => run({ from: f, to: t })} />
	<!-- svelte-ignore a11y_no_static_element_interactions (Escape hides the tooltip: WCAG 1.4.13) -->
	<div
		class="has-tip clear"
		class:hidden={!set}
		class:tip-off={clearTipOff}
		onkeydown={(e) => e.key === 'Escape' && (clearTipOff = true)}
		onpointerleave={() => (clearTipOff = false)}
		onfocusout={() => (clearTipOff = false)}
	>
		<button type="button" class="ghost icon-button" aria-label="Clear all" onclick={clearAll}><Icon name="clear" /></button>
		<span class="tooltip" aria-hidden="true">Clear all</span>
	</div>
	<button type="submit"><Icon name="search" />Search</button>
	{#if problem}<p class="problem" id="{uid}-problem" role="alert">{problem}</p>{/if}
</form>

<style>
	.search {
		position: relative;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-sm);
		min-width: 0;
	}
	.search > :global(.suggest) {
		flex: 1 1 var(--field-wide);
	}
	.help .panel {
		position: absolute;
		right: 0;
		top: 100%;
		z-index: var(--layer-overlay);
		width: var(--dialog-width);
		max-width: var(--panel-max);
		margin-top: var(--space-2xs);
		padding: var(--space-lg);
		background: var(--surface-2);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
		font-size: var(--text-sm);
	}
	.panel p {
		margin: 0 0 var(--space-md);
		color: var(--muted);
	}
	.keys {
		display: grid;
		gap: var(--space-sm);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.keys li,
	.key {
		display: flex;
		flex-direction: column;
	}
	.key {
		font-weight: var(--weight-strong);
	}
	.who {
		color: var(--muted);
		font-size: var(--text-xs);
	}
	.what {
		color: var(--muted);
	}
	code {
		font-family: var(--font-mono);
	}
	.help {
		display: inline-flex;
	}
	/* The help and the calendar open under the whole row, at its end. */
	.search :global(.dates) {
		position: static;
	}
	.search :global(.calendar) {
		left: auto;
		right: 0;
	}
	/* Clear all keeps its place when there's nothing to clear, so the row
	   doesn't move when it comes. */
	.clear.hidden {
		visibility: hidden;
	}
	.problem {
		flex-basis: 100%;
		margin: 0;
		color: var(--danger-text);
		font-size: var(--text-sm);
	}
	/* Phones: the box has a row of its own, with the controls under it. */
	@media (max-width: 47.99rem) {
		.search {
			width: 100%;
			justify-content: flex-end;
		}
		.search > :global(.suggest) {
			flex-basis: 100%;
		}
		/* The calendar fits under the row, which is narrower than the screen. */
		.search :global(.calendar) {
			max-width: 100%;
		}
	}
</style>
