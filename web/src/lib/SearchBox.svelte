<script lang="ts">
	// R33a: one search box, then Help, the dates and Clear all (M21). It
	// follows the ARIA combobox pattern: clicking into the empty box (or
	// between words) lists every key; typing a key's value suggests its
	// values, and typing a word with no key suggests searching it as text,
	// then the filters whose values contain it (search.ts). Arrow keys move
	// through the suggestions, Enter picks one (or, with none picked,
	// searches), Escape closes the list, and the number of suggestions is
	// announced. Picking a value searches,
	// as Enter does; picking a key puts it in the box, ready for its value.
	// The search applies to the view it's on (the list, the map, an alias, a
	// job, the print page), and is kept in its address, Clear all included,
	// so Back restores it.
	import { goto } from '$app/navigation';
	import DateRange from './DateRange.svelte';
	import Icon from './Icon.svelte';
	import { boxText, examples, keysFor, parseBox, suggestions, type Suggestion, type Suggestions, type Values } from './search.ts';
	import { searchValues } from './searchValues.ts';

	let {
		action,
		query,
		admin,
		categories,
		timeZone
	}: { action: string; query: URLSearchParams; admin: boolean; categories: string[]; timeZone: string } = $props();

	const uid = $props.id();
	let text = $state('');
	let problem = $state('');
	let input: HTMLInputElement;

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
	let list: Suggestions = $state({ options: [], what: 'suggestion' });
	let open = $state(false);
	let active = $state(-1);
	let announce = $state('');
	let help = $state(false);
	let helpTipOff = $state(false);
	let clearTipOff = $state(false);
	let helpWrap: HTMLDivElement;
	let quiet = false;
	// Whether the list follows the word at the caret (not just the keys, and not
	// closed), so it's worked out again when the values come.
	let following = false;

	// Grouped for the list: a run of options with the same heading.
	const groups = $derived.by(() => {
		const out: { section?: string; options: { o: Suggestion; i: number }[] }[] = [];
		list.options.forEach((o, i) => {
			const section = o.kind === 'text' ? undefined : o.section;
			const last = out.at(-1);
			if (last && last.section === section) last.options.push({ o, i });
			else out.push({ section, options: [{ o, i }] });
		});
		return out;
	});

	function load() {
		searchValues()
			.then((v) => {
				values = { ...v, category: categories };
				if (following) suggest(); // the word being typed, again with them
			})
			.catch(() => {}); // suggestions without the API's values: keys and text
	}

	function setList(next: Suggestions) {
		list = next;
		active = -1;
		open = next.options.length > 0;
		const n = next.options.length;
		announce = n ? `${n} ${next.what}${n === 1 ? '' : 's'}` : '';
	}

	function close() {
		following = false;
		open = false;
		list = { options: [], what: 'suggestion' };
		active = -1;
	}

	/** Suggestions for the word at the caret. */
	function suggest() {
		following = true;
		problem = '';
		setList(suggestions(text, input.selectionStart ?? text.length, admin, { ...values, category: categories }));
	}

	// Into the box: the suggestions for the word at the caret, which, in an
	// empty box or between words, are every key. (Not always the keys: WebKit
	// can send focus after the first input, and the list must follow that.)
	function focus() {
		load();
		if (quiet) {
			quiet = false;
			return;
		}
		suggest();
	}

	async function place(next: string, caret: number) {
		text = next;
		await Promise.resolve();
		input.focus();
		input.setSelectionRange(caret, caret);
	}

	async function pick(o: Suggestion) {
		if (o.kind === 'text') return run();
		const w = list.target;
		if (o.kind === 'key') {
			const k = `${o.key}:`;
			if (w) await place(text.slice(0, w.start) + k + text.slice(w.end), w.start + k.length);
			else {
				// At the cursor, as a word of its own.
				const at = input.selectionStart ?? text.length;
				const before = text.slice(0, at);
				const ins = (before && !/\s$/.test(before) ? ' ' : '') + k;
				await place(before + ins + text.slice(at), at + ins.length);
			}
			// Now its values.
			return suggest();
		}
		// A value replaces the word it was suggested for, and searches.
		const start = w ? w.start : text.length;
		const after = text.slice(w ? w.end : text.length);
		const ins = o.insert + (after.startsWith(' ') ? '' : ' ');
		await place(text.slice(0, start) + ins + after, start + ins.length);
		run();
	}

	function keydown(ev: KeyboardEvent) {
		if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
			ev.preventDefault();
			if (!open) return suggest();
			const n = list.options.length;
			active = ev.key === 'ArrowDown' ? (active + 1) % n : active <= 0 ? n - 1 : active - 1;
			document.getElementById(optionId(list.options[active]))?.scrollIntoView({ block: 'nearest' });
		} else if (ev.key === 'Enter' && open && active >= 0) {
			ev.preventDefault();
			pick(list.options[active]);
		} else if (ev.key === 'Escape' && open) {
			ev.preventDefault();
			close();
		} else if (['Home', 'End', 'ArrowLeft', 'ArrowRight'].includes(ev.key) && open) {
			setTimeout(() => open && suggest(), 0); // the caret moved: follow it
		}
	}

	/** Searches what's in the box, with these dates. */
	function run(dates = { from, to }) {
		close();
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
		close();
		go(new URLSearchParams());
		quiet = document.activeElement !== input;
		input.focus();
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

	const optionId = (o: Suggestion) => `${uid}-opt-${list.options.indexOf(o)}`;
	const shownValue = (insert: string, key: string) => insert.slice(key.length + 1);
</script>

<svelte:document onpointerdown={outside} />

<form class="search" role="search" aria-label="Search messages" onsubmit={submit}>
	<div class="box">
		<label class="field">
			<Icon name="search" />
			<span class="visually-hidden">Search</span>
			<input
				bind:this={input}
				bind:value={text}
				name="search"
				type="text"
				role="combobox"
				autocomplete="off"
				spellcheck="false"
				aria-autocomplete="list"
				aria-expanded={open}
				aria-controls="{uid}-list"
				aria-activedescendant={open && active >= 0 ? `${uid}-opt-${active}` : undefined}
				aria-describedby={problem ? `${uid}-problem` : undefined}
				placeholder={'Text, or alias:"Station 1", priority:emergency …'}
				oninput={suggest}
				onkeydown={keydown}
				onfocus={focus}
				onclick={suggest}
				onblur={close}
			/>
		</label>
		<div id="{uid}-list" class="options" role="listbox" aria-label="Search suggestions" hidden={!open}>
			{#each groups as g, gi (gi)}
				<ul role={g.section ? 'group' : 'presentation'} aria-labelledby={g.section ? `${uid}-sec-${gi}` : undefined}>
					{#if g.section}<li class="section" role="presentation" id="{uid}-sec-{gi}">{g.section}</li>{/if}
					{#each g.options as { o, i } (o.id)}
						<!-- svelte-ignore a11y_click_events_have_key_events (the combobox handles the keys) -->
						<li
							id="{uid}-opt-{i}"
							role="option"
							aria-selected={i === active}
							class:active={i === active}
							onmousedown={(e) => e.preventDefault()}
							onclick={() => pick(o)}
						>
							{#if o.kind === 'text'}
								<span class="text">Search text: {o.text}</span>
							{:else if o.kind === 'key'}
								<code>{o.key}:</code><span class="hint">{o.hint}</span>
							{:else}
								<span><span class="k">{o.key}:</span>{shownValue(o.insert, o.key)}</span>
							{/if}
						</li>
					{/each}
				</ul>
			{/each}
		</div>
	</div>
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
		</div>
	</div>
	<DateRange {from} {to} {timeZone} onchange={(f, t) => run({ from: f, to: t })} />
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
	<div class="visually-hidden" aria-live="polite">{announce}</div>
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
	.box {
		position: relative;
		flex: 1 1 var(--field-wide);
		min-width: 0;
	}
	.field {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-height: var(--target);
		padding: 0 var(--space-md);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
		background: var(--surface-2);
		color: var(--muted);
	}
	.field:focus-within {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: var(--focus-offset);
	}
	.field input {
		min-height: 0;
		padding: var(--space-xs) 0;
		border: 0;
		background: transparent;
		color: var(--fg);
	}
	.field input:focus-visible {
		outline: none;
	}
	.field input::placeholder {
		color: var(--muted);
		opacity: 1;
	}
	.options {
		position: absolute;
		left: 0;
		right: 0;
		top: 100%;
		z-index: var(--layer-overlay);
		max-height: var(--suggestions-height);
		overflow-y: auto;
		margin: var(--space-2xs) 0 0;
		padding: var(--space-2xs);
		background: var(--surface-2);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
	}
	.options ul {
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.options ul + ul {
		margin-top: var(--space-2xs);
		border-top: var(--border-width) solid var(--border);
	}
	.options .section {
		padding: var(--space-xs) var(--space-md) var(--space-3xs);
		color: var(--muted);
		font-size: var(--text-xxs);
		font-weight: var(--weight-strong);
		letter-spacing: var(--badge-tracking);
		text-transform: var(--badge-case);
	}
	.options li[role='option'] {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-height: var(--target);
		padding: 0 var(--space-md);
		border-radius: var(--radius-small);
		cursor: pointer;
	}
	.options li[role='option']:hover {
		background: var(--hover);
	}
	.options li.active {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	.options code {
		min-width: var(--label-min);
		font-family: var(--font-mono);
		font-weight: var(--weight-strong);
	}
	.options .hint,
	.options .k {
		color: var(--muted);
	}
	.options .hint {
		font-size: var(--text-sm);
	}
	.options li.active .hint,
	.options li.active .k {
		color: inherit;
	}
	.options .text {
		font-weight: var(--weight-strong);
		overflow-wrap: anywhere;
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
		.box {
			flex-basis: 100%;
		}
	}
</style>
