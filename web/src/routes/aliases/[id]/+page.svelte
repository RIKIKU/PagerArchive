<script lang="ts">
	import { page } from '$app/state';
	import { api, type AliasView, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/search.ts';
	import AgencyIcon from '#lib/AgencyIcon.svelte';
	import AliasHistory from '#lib/AliasHistory.svelte';
	import AliasRequestForm from '#lib/AliasRequestForm.svelte';
	import MessageList from '#lib/MessageList.svelte';
	import RequestCard from '#lib/RequestCard.svelte';
	import { setTitle } from '#lib/here.svelte.ts';

	// R35e: the alias, then the messages from the capcodes it covers. Pages to
	// an ignored alias are shown here: that's what was asked for. From M6:
	// admins edit it (R53) and see its history (R57, admins only); users
	// request a change or its deletion (R54) and see their waiting request.
	let meta: Meta | undefined = $state();
	let alias: AliasView | undefined = $state();
	let error = $state('');
	let notice = $state('');
	let asking: 'change' | 'delete' | undefined = $state();
	const id = $derived(page.params.id ?? '');
	const query = $derived(criteria(page.url));
	const tz = $derived(meta?.time_zone ?? 'UTC');
	const admin = $derived(meta?.role === 'admin');

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.alias(id)
			.then((a) => (alias = a))
			.catch((e) => (error = e.message));
	}
	$effect(() => {
		error = '';
		load();
	});

	$effect(() => {
		if (alias) setTitle(alias.name);
	});

	function done(msg: string) {
		asking = undefined;
		notice = msg;
		load();
	}
</script>

<svelte:head><title>{alias?.name ?? 'Alias'} · PagerArchive</title></svelte:head>

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if alias}
	<section class="about" aria-labelledby="alias-name">
		<div class="title">
			{#if alias.icon}<AgencyIcon src={alias.icon} alt={alias.agency ?? ''} large />{/if}
			<h1 id="alias-name" style:--agency={alias.colour ?? 'transparent'}>{alias.name}</h1>
		</div>
		<dl>
			<dt>{alias.pattern.includes('%') || alias.pattern.includes('_') ? 'Pattern' : 'Capcode'}</dt>
			<dd class="mono">{alias.pattern}</dd>
			<dt>Agency</dt>
			<dd>{alias.agency ?? 'None (text only)'}</dd>
			<dt>Colour</dt>
			<dd>
				{#if alias.colour}<span class="swatch" style:background={alias.colour} aria-hidden="true"></span>{alias.colour}
				{:else}None{/if}
			</dd>
			<dt>Ignored</dt>
			<dd>{alias.ignored ? 'Yes: its pages are hidden from the main list' : 'No'}</dd>
		</dl>
		{#if !/[%_]/.test(alias.pattern)}
			<p><a href="/reports/capcode?capcode={alias.pattern}">Report for this capcode</a></p>
		{:else}
			<p><a href="/reports/capcode?{new URLSearchParams({ alias: alias.name })}">Report for this alias</a></p>
		{/if}
		<div aria-live="polite">
			{#if notice}<p class="ok" role="status">{notice}</p>{/if}
		</div>
		{#if admin}
			<p><a class="button secondary" href="/admin/aliases/{alias.id}">Edit alias</a></p>
		{:else if meta && alias.my_request}
			<RequestCard r={alias.my_request} {tz} level={2}>
				{#snippet actions()}<a href="/requests">Edit or withdraw it in My requests</a>{/snippet}
			</RequestCard>
		{:else if meta && asking}
			<AliasRequestForm kind={asking} alias={alias} onDone={done} onCancel={() => (asking = undefined)} />
		{:else if meta}
			<div class="asks">
				<button type="button" class="secondary" onclick={() => ((asking = 'change'), (notice = ''))}>Request a change</button>
				<button type="button" class="secondary" onclick={() => ((asking = 'delete'), (notice = ''))}>Request deletion</button>
			</div>
		{/if}
	</section>
	{#if admin && alias.history}
		<AliasHistory history={alias.history} {tz} />
	{/if}
	<div class="listbox"><MessageList {query} base={{ alias_id: String(alias.id), ignored: 'true' }} level={2} /></div>
{/if}

<style>
	.about {
		max-width: var(--measure);
	}
	.title {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}
	h1 {
		margin: 0 0 var(--space-md);
		border-left: var(--agency-bar) solid var(--agency);
		padding-left: var(--space-xs);
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
		overflow-wrap: anywhere;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.swatch {
		display: inline-block;
		width: var(--swatch-size-small);
		height: var(--swatch-size-small);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-small);
		vertical-align: middle;
		margin-right: var(--space-xs);
	}
	.listbox {
		margin-top: var(--space-xl);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		overflow: clip;
	}
	.asks {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin: var(--space-sm) 0;
	}
</style>
