<script lang="ts">
	// R33a's suggestion control (M21): a box that follows the ARIA combobox
	// pattern, used by the search box and by the capcode report (R38). What it
	// suggests comes from suggest (search.ts): arrow keys move through the
	// suggestions, Enter picks one, Escape closes the list, and the number of
	// suggestions is announced. Picking a key puts it in the box at the cursor,
	// ready for its value, and suggests its values; picking a value replaces
	// the word it was suggested for. Then onpick is told, so the search box
	// can search; Enter with nothing picked is left to the form.
	import Icon from './Icon.svelte';
	import type { IconName } from './icons.ts';
	import type { Suggestion, Suggestions } from './search.ts';

	let {
		text = $bindable(''),
		suggest: suggestFor,
		onpick,
		onfocus,
		oninput,
		label,
		showLabel = false,
		icon,
		placeholder,
		listLabel,
		describedby
	}: {
		text?: string;
		/** The suggestions for the word at the caret. */
		suggest: (text: string, caret: number) => Suggestions;
		/** A value or the text was picked (not a key). */
		onpick?: (o: Suggestion) => void;
		onfocus?: () => void;
		oninput?: () => void;
		label: string;
		/** The label above the box; else it's for screen readers only. */
		showLabel?: boolean;
		icon?: IconName;
		placeholder?: string;
		listLabel: string;
		describedby?: string;
	} = $props();

	const uid = $props.id();
	let input: HTMLInputElement;
	let list: Suggestions = $state({ options: [], what: 'suggestion' });
	let open = $state(false);
	let active = $state(-1);
	let announce = $state('');
	let quiet = false;
	// Whether the list follows the word at the caret (it's open), so it's
	// worked out again when what it's suggested from changes (refresh).
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

	function setList(next: Suggestions) {
		list = next;
		active = -1;
		open = next.options.length > 0;
		const n = next.options.length;
		announce = n ? `${n} ${next.what}${n === 1 ? '' : 's'}` : '';
	}

	/** Closes the list. */
	export function close() {
		following = false;
		open = false;
		list = { options: [], what: 'suggestion' };
		active = -1;
	}

	/** Suggestions for the word at the caret. */
	function suggest() {
		following = true;
		setList(suggestFor(text, input.selectionStart ?? text.length));
	}

	/** What's suggested from has changed (the values came): the list again. */
	export function refresh() {
		if (following) suggest();
	}

	/** Focus to the box; quietly, without opening the list. */
	export function focus(withoutList = false) {
		quiet = withoutList && document.activeElement !== input;
		input.focus();
	}

	// Into the box: the suggestions for the word at the caret. (Not only on
	// click: WebKit can send focus after the first input, and the list must
	// follow that.)
	function focused() {
		onfocus?.();
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
		if (o.kind === 'text') {
			close();
			return onpick?.(o);
		}
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
		// A value replaces the word it was suggested for.
		const start = w ? w.start : text.length;
		const after = text.slice(w ? w.end : text.length);
		const ins = o.insert + (after.startsWith(' ') ? '' : ' ');
		await place(text.slice(0, start) + ins + after, start + ins.length);
		close();
		onpick?.(o);
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

	const optionId = (o: Suggestion) => `${uid}-opt-${list.options.indexOf(o)}`;
	const shownValue = (insert: string, key: string) => insert.slice(key.length + 1);
</script>

<div class="suggest">
	{#if showLabel}<label class="above" for="{uid}-input">{label}</label>{/if}
	<svelte:element this={showLabel ? 'div' : 'label'} class="field">
		{#if icon}<Icon name={icon} />{/if}
		{#if !showLabel}<span class="visually-hidden">{label}</span>{/if}
		<input
			bind:this={input}
			bind:value={text}
			id="{uid}-input"
			type="text"
			role="combobox"
			autocomplete="off"
			spellcheck="false"
			aria-autocomplete="list"
			aria-expanded={open}
			aria-controls="{uid}-list"
			aria-activedescendant={open && active >= 0 ? `${uid}-opt-${active}` : undefined}
			aria-describedby={describedby}
			{placeholder}
			oninput={() => {
				oninput?.();
				suggest();
			}}
			onkeydown={keydown}
			onfocus={focused}
			onclick={suggest}
			onblur={close}
		/>
	</svelte:element>
	<div id="{uid}-list" class="options" role="listbox" aria-label={listLabel} hidden={!open}>
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
	<div class="visually-hidden" aria-live="polite">{announce}</div>
</div>

<style>
	.suggest {
		position: relative;
		min-width: 0;
	}
	.above {
		display: block;
		margin-bottom: var(--space-3xs);
		font-size: var(--text-sm);
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
</style>
