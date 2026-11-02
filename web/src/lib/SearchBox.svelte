<script lang="ts">
	// R33a: one search box, with From and To beside it. It follows the ARIA
	// combobox pattern: as a key or a value is typed, matching ones are
	// suggested; arrow keys move through them, Enter picks one (or, with
	// none picked, searches), Escape closes the list, and the number of
	// suggestions is announced. A help control lists every key with an
	// example. The search applies to the view it's on (the list, the map,
	// an alias, a job, the print page), and is kept in its address.
	import { goto } from '$app/navigation';
	import { api } from './api.ts';
	import DateRange from './DateRange.svelte';
	import Icon from './Icon.svelte';
	import { boxText, examples, flagValues, isValues, keysFor, parseBox, priorities, show, words, type Key, type Word } from './search.ts';

	let { action, query, admin, categories }: { action: string; query: URLSearchParams; admin: boolean; categories: string[] } = $props();

	const uid = $props.id();
	let text = $derived(boxText(query));
	let problem = $state('');
	let dates = $state(false);
	let input: HTMLInputElement;

	type Option = { label: string; insert: string; key?: Key };
	let options: Option[] = $state([]);
	let open = $state(false);
	let active = $state(-1);
	let announce = $state('');
	let target: Word | undefined;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let seq = 0;

	const fromAPI: Key[] = ['alias', 'agency', 'group'];
	const exactKeys: Key[] = ['alias', 'agency', 'group', 'category'];

	function setOptions(list: Option[]) {
		options = list;
		active = -1;
		open = list.length > 0;
		announce = list.length ? `${list.length} suggestion${list.length === 1 ? '' : 's'}` : '';
	}

	function close() {
		clearTimeout(timer);
		seq++;
		open = false;
		options = [];
		active = -1;
	}

	/** Suggestions for the word at the caret. */
	function suggest() {
		problem = '';
		const caret = input.selectionStart ?? text.length;
		const w = words(text, admin).find((x) => x.start <= caret && caret <= x.end);
		target = w;
		clearTimeout(timer);
		if (!w || (!w.key && (w.quoted || !w.value))) return close();
		if (!w.key) {
			const typed = w.value.toLowerCase();
			return setOptions(keysFor(admin).filter((k) => k.startsWith(typed) && k !== typed).map((k) => ({ label: `${k}:`, insert: `${k}:`, key: k })));
		}
		const typed = w.value.toLowerCase();
		const key = w.key;
		const values = (list: readonly string[]) => list.filter((v) => v.toLowerCase().includes(typed));
		const fixed: Partial<Record<Key, readonly string[]>> = {
			priority: priorities.map((p) => p.toLowerCase()),
			is: isValues,
			flag: admin ? flagValues : [],
			category: [...categories, 'Other']
		};
		if (fixed[key]) return setOptions(values(fixed[key]!).map((v) => ({ label: v, insert: `${key}:${show(v, exactKeys.includes(key))}` })));
		if (!fromAPI.includes(key)) return close(); // capcode:, job: and or: have none
		const mine = ++seq;
		timer = setTimeout(async () => {
			try {
				const found = await api.suggest(key as 'alias' | 'agency' | 'group', w.value);
				if (mine === seq) setOptions(found.map((v) => ({ label: v, insert: `${key}:${show(v, true)}` })));
			} catch {
				if (mine === seq) close();
			}
		}, 150);
	}

	async function pick(o: Option) {
		const w = target;
		if (!w) return;
		const after = text.slice(w.end);
		const spaced = o.key ? o.insert : `${o.insert}${after.startsWith(' ') ? '' : ' '}`;
		text = text.slice(0, w.start) + spaced + after;
		const caret = w.start + spaced.length;
		close();
		await Promise.resolve();
		input.focus();
		input.setSelectionRange(caret, caret);
		// A key picked: now its values.
		if (o.key) suggest();
	}

	function keydown(ev: KeyboardEvent) {
		if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
			if (!open || !options.length) {
				if (ev.key === 'ArrowDown') suggest();
				return;
			}
			ev.preventDefault();
			const n = options.length;
			active = ev.key === 'ArrowDown' ? (active + 1) % n : (active - 1 + n) % n;
		} else if (ev.key === 'Enter' && open && active >= 0) {
			ev.preventDefault();
			pick(options[active]);
		} else if (ev.key === 'Escape' && open) {
			ev.preventDefault();
			close();
		}
	}

	function submit(ev: SubmitEvent) {
		ev.preventDefault();
		close();
		const parsed = parseBox(text, admin);
		problem = parsed.problem;
		if (problem) return;
		const form = new FormData(ev.currentTarget as HTMLFormElement);
		const params = parsed.params;
		for (const k of ['from', 'to']) {
			const v = String(form.get(k) ?? '');
			if (v) params.set(k, v);
		}
		const s = params.toString();
		// Focus stays in the box; the results start at the top.
		goto(s ? `${action}?${s}` : action, { reset: false }).then(() => window.scrollTo(0, 0));
	}

	const optionId = (i: number) => `${uid}-opt-${i}`;
</script>

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
				aria-activedescendant={open && active >= 0 ? optionId(active) : undefined}
				aria-describedby="{uid}-hint{problem ? ` ${uid}-problem` : ''}"
				placeholder={'Text, or alias:"Station 1", priority:emergency …'}
				oninput={suggest}
				onkeydown={keydown}
				onclick={suggest}
				onblur={close}
			/>
		</label>
		<ul id="{uid}-list" class="options" role="listbox" aria-label="Search suggestions" hidden={!open}>
			{#each options as o, i (o.insert)}
				<!-- svelte-ignore a11y_click_events_have_key_events (the combobox handles the keys) -->
				<li id={optionId(i)} role="option" aria-selected={i === active} class:active={i === active} onmousedown={(e) => e.preventDefault()} onclick={() => pick(o)}>
					{o.label}
				</li>
			{/each}
		</ul>
	</div>
	<div class="dates" class:shown={dates} id="{uid}-dates">
		<DateRange from={query.get('from') ?? ''} to={query.get('to') ?? ''} />
	</div>
	<div class="buttons">
		<button class="secondary when" type="button" aria-expanded={dates} aria-controls="{uid}-dates" onclick={() => (dates = !dates)}
			><Icon name="calendar" />Dates</button
		>
		<details class="help">
			<summary class="button ghost"><Icon name="help" />Help</summary>
			<div class="panel">
				<p id="{uid}-hint">
					Type words to find pages containing every one of them; words in quotes, such as "check hoses", must be together, in that
					order. Filters narrow the search: a filter is a key and a value. A value with spaces goes in quotes; in quotes it must match
					exactly, and without them it matches any value containing it. The same key twice means either value.
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
		</details>
		<button type="submit"><Icon name="search" />Search</button>
	</div>
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
		list-style: none;
		background: var(--surface-2);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
	}
	.options li {
		display: flex;
		align-items: center;
		min-height: var(--target);
		padding: 0 var(--space-md);
		border-radius: var(--radius-small);
		cursor: pointer;
	}
	.options li:hover {
		background: var(--hover);
	}
	.options li.active {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
		font-weight: var(--weight-strong);
	}
	.dates {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}
	.dates :global(label) {
		display: flex;
		flex-direction: row;
		align-items: center;
		gap: var(--space-xs);
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.dates :global(input) {
		width: auto;
		color: var(--fg);
	}
	.dates :global(.range-error) {
		color: var(--danger-text);
	}
	.buttons {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}
	.when {
		display: none;
	}
	.help {
		position: relative;
	}
	.help summary {
		list-style: none;
	}
	.help summary::-webkit-details-marker {
		display: none;
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
	.problem {
		flex-basis: 100%;
		margin: 0;
		color: var(--danger-text);
		font-size: var(--text-sm);
	}
	/* Phones: the dates open under the box. */
	@media (max-width: 47.99rem) {
		.search {
			width: 100%;
		}
		.box {
			flex-basis: 100%;
		}
		.when {
			display: inline-flex;
		}
		.dates {
			display: none;
			flex-wrap: wrap;
			order: 2;
			flex-basis: 100%;
		}
		.dates.shown {
			display: flex;
		}
		.buttons {
			flex-basis: 100%;
			justify-content: flex-end;
		}
	}
</style>
