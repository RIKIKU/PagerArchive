<script lang="ts">
	import { untrack } from 'svelte';
	import type { Meta } from './api.ts';
	import { labels, isFiltered } from './filters.ts';

	let { query, meta }: { query: URLSearchParams; meta: Meta } = $props();

	// Starts open on wide screens or when a search is active, collapsed on
	// phones; after that the viewer decides.
	let open = $state(untrack(() => isFiltered(query)) || window.innerWidth >= 900);
	const v = (k: string) => query.get(k) ?? '';
	const checked = (k: string) => query.get(k) === 'true';
</script>

<details class="filters" bind:open>
	<summary>Search{#if isFiltered(query)} <span class="active">(active)</span>{/if}</summary>
	<!-- A GET form: the criteria live in the URL (shareable, survive reloads). -->
	<form method="GET" action="/" role="search" aria-label="Search messages">
		<div class="grid">
			<label class="wide">{labels.q}<input name="q" type="search" value={v('q')} autocomplete="off" /></label>
			<label>{labels.capcode}<input name="capcode" inputmode="numeric" pattern="[0-9]{'{1,7}'}" value={v('capcode')} /></label>
			<label>{labels.alias}<input name="alias" value={v('alias')} /></label>
			<label>{labels.agency}<input name="agency" value={v('agency')} /></label>
			<label>{labels.group}<input name="group" value={v('group')} /></label>
			<label>{labels.job}<input name="job" value={v('job')} /></label>
			<label
				>{labels.priority}
				<select name="priority" value={v('priority')}>
					<option value="">Any</option>
					<option>Emergency</option>
					<option>Non-emergency</option>
					<option>Admin</option>
					<option value="none">None</option>
				</select>
			</label>
			<label
				>{labels.category}
				<select name="category" value={v('category')}>
					<option value="">Any</option>
					{#each meta.categories as c (c)}<option>{c}</option>{/each}
				</select>
			</label>
			<label>{labels.from}<input name="from" type="date" value={v('from')} /></label>
			<label>{labels.to}<input name="to" type="date" value={v('to')} /></label>
		</div>
		<fieldset>
			<legend class="visually-hidden">Options</legend>
			<label class="check"
				><input type="checkbox" name="unaliased" value="true" checked={checked('unaliased')} /> {labels.unaliased}</label
			>
			<label class="check"
				><input type="checkbox" name="ignored" value="true" checked={checked('ignored')} /> {labels.ignored}</label
			>
		</fieldset>
		{#if meta.role === 'admin'}
			<fieldset>
				<legend>Flags (admins only)</legend>
				{#each ['parse_failed', 'address_unsplit', 'location_mismatch'] as k (k)}
					<label class="check"><input type="checkbox" name={k} value="true" checked={checked(k)} /> {labels[k]}</label>
				{/each}
			</fieldset>
		{/if}
		<div class="actions">
			<button type="submit">Search</button>
			{#if isFiltered(query)}<a href="/">Clear</a>{/if}
		</div>
	</form>
</details>

<style>
	.filters {
		border-bottom: 1px solid var(--border);
		padding: 0.5rem 0.75rem;
	}
	summary {
		cursor: pointer;
		font-weight: 600;
		padding: 0.25rem 0;
	}
	.active {
		color: var(--accent);
		font-weight: 400;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(10rem, 1fr));
		gap: 0.5rem 0.75rem;
		margin-top: 0.5rem;
	}
	.wide {
		grid-column: 1 / -1;
	}
	label {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		font-size: 0.85rem;
		min-width: 0;
	}
	label.check {
		flex-direction: row;
		align-items: center;
		gap: 0.4rem;
	}
	fieldset {
		border: none;
		padding: 0;
		margin: 0.5rem 0 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 1rem;
	}
	legend {
		font-size: 0.85rem;
		font-weight: 600;
		padding: 0;
		margin-bottom: 0.25rem;
	}
	.actions {
		display: flex;
		align-items: center;
		gap: 1rem;
		margin-top: 0.75rem;
	}
</style>
