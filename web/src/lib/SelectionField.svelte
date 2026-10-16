<script lang="ts">
	// R48: an integration's selection is one field. The user types an alias
	// name or a capcode and picks from suggestions showing both. The server
	// decides what a capcode becomes: its own alias if it has one, otherwise
	// the capcode, with the range alias covering it named beside it, so
	// typing a capcode never selects a range.
	import { api, type Suggestion } from './api.ts';

	type AliasRef = { id: number; pattern: string; name: string };
	let {
		id,
		capcodes = $bindable(),
		aliases = $bindable(),
		coveredBy = $bindable()
	}: {
		id: string;
		capcodes: string[];
		aliases: AliasRef[];
		coveredBy: Record<string, string>;
	} = $props();

	let query = $state('');
	let options: Suggestion[] = $state([]);
	let active = $state(0);
	let open = $state(false);
	let searched = $state('');
	let timer: ReturnType<typeof setTimeout> | undefined;
	let seq = 0;

	const chosen = (s: Suggestion) =>
		s.kind === 'alias' ? aliases.some((a) => a.id === s.alias_id) : capcodes.includes(s.capcode ?? '');
	const shown = $derived(options.filter((s) => !chosen(s)));

	function typed() {
		clearTimeout(timer);
		const q = query.trim();
		if (!q) {
			options = [];
			open = false;
			return;
		}
		timer = setTimeout(async () => {
			const mine = ++seq;
			try {
				const out = await api.selection(q);
				if (mine !== seq) return; // an older answer
				options = out;
				searched = q;
				active = 0;
				open = true;
			} catch {
				options = [];
			}
		}, 150);
	}

	function choose(s: Suggestion) {
		if (s.kind === 'alias' && s.alias_id != null) {
			aliases = [...aliases, { id: s.alias_id, name: s.name ?? '', pattern: s.pattern ?? '' }];
		} else if (s.capcode) {
			capcodes = [...capcodes, s.capcode];
			if (s.covered_by) coveredBy = { ...coveredBy, [s.capcode]: s.covered_by };
		}
		query = '';
		options = [];
		open = false;
	}

	function key(ev: KeyboardEvent) {
		if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
			if (!shown.length) return;
			ev.preventDefault();
			open = true;
			active = (active + (ev.key === 'ArrowDown' ? 1 : shown.length - 1)) % shown.length;
		} else if (ev.key === 'Enter') {
			// Never submits the form from here.
			if (query.trim()) ev.preventDefault();
			if (open && shown[active]) choose(shown[active]);
		} else if (ev.key === 'Escape' && open) {
			ev.preventDefault();
			open = false;
		}
	}

	const aliasLabel = (a: { name?: string; pattern?: string }) => `${a.name} (${a.pattern})`;
	const optionId = (i: number) => `${id}-opt-${i}`;
</script>

<div class="field">
	<label for={id}>Capcodes and aliases</label>
	{#if aliases.length || capcodes.length}
		<ul class="chosen" aria-label="Chosen capcodes and aliases">
			{#each aliases as a (a.id)}
				<li data-testid="chosen">
					<span>{aliasLabel(a)}</span>
					<button type="button" class="remove" aria-label="Remove {aliasLabel(a)}" onclick={() => (aliases = aliases.filter((x) => x.id !== a.id))}
						>×</button
					>
				</li>
			{/each}
			{#each capcodes as c (c)}
				<li data-testid="chosen">
					<span class="mono">{c}</span>
					{#if coveredBy[c]}<span class="muted">in {coveredBy[c]}</span>{/if}
					<button type="button" class="remove" aria-label="Remove capcode {c}" onclick={() => (capcodes = capcodes.filter((x) => x !== c))}>×</button>
				</li>
			{/each}
		</ul>
	{/if}
	<input
		{id}
		type="text"
		role="combobox"
		autocomplete="off"
		spellcheck="false"
		aria-autocomplete="list"
		aria-expanded={open && shown.length > 0}
		aria-controls="{id}-list"
		aria-activedescendant={open && shown[active] ? optionId(active) : undefined}
		aria-describedby="{id}-help"
		placeholder="Station 1 or 0100001"
		bind:value={query}
		oninput={typed}
		onkeydown={key}
		onblur={() => (open = false)}
		onfocus={() => (open = options.length > 0)}
	/>
	<ul id="{id}-list" role="listbox" aria-label="Suggestions" class="options" hidden={!open || shown.length === 0}>
		{#each shown as s, i (s.kind + (s.alias_id ?? s.capcode))}
			<!-- svelte-ignore a11y_click_events_have_key_events (the combobox input handles the keys) -->
			<li
				id={optionId(i)}
				role="option"
				aria-selected={i === active}
				class:active={i === active}
				onmousedown={(e) => e.preventDefault()}
				onclick={() => choose(s)}
			>
				{#if s.kind === 'alias'}
					{aliasLabel(s)}{#if s.range}<span class="muted"> range</span>{/if}
				{:else}
					<span class="mono">{s.capcode}</span>
					<span class="muted">capcode{s.covered_by ? ` · in ${s.covered_by}` : ''}</span>
				{/if}
			</li>
		{/each}
	</ul>
	<p class="visually-hidden" role="status">
		{#if open && searched}{shown.length === 0 ? 'No suggestions' : `${shown.length} suggestion${shown.length === 1 ? '' : 's'}`}{/if}
	</p>
	<p id="{id}-help" class="help">
		Type an alias name or a capcode, then choose from the list. A capcode with its own alias is chosen as that alias.
	</p>
</div>

<style>
	.field {
		margin: 0.5rem 0;
	}
	label {
		display: block;
		margin-bottom: 0.2rem;
	}
	input {
		width: 100%;
	}
	.chosen {
		list-style: none;
		padding: 0;
		margin: 0 0 0.35rem;
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
	}
	.chosen li {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0 0 0.6rem;
		background: var(--bg);
		overflow-wrap: anywhere;
	}
	.remove {
		border: 0;
		background: none;
		color: var(--fg);
		font-size: var(--text-lg);
		line-height: 1;
		padding: 0.2rem 0.5rem;
		min-height: 0;
		cursor: pointer;
	}
	.options {
		list-style: none;
		padding: 0;
		margin: 0.25rem 0 0;
		max-height: 14rem;
		overflow-y: auto;
		border: 1px solid var(--border);
		border-radius: 0.3rem;
		background: var(--bg);
	}
	.options li {
		padding: 0.35rem 0.5rem;
		cursor: pointer;
		overflow-wrap: anywhere;
	}
	.options li.active {
		background: var(--highlight);
	}
	.mono {
		font-family: var(--mono);
	}
	.muted {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.help {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.25rem 0;
	}
</style>
