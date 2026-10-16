<script lang="ts">
	import { page } from '$app/state';
	import { api, type AliasView, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/filters.ts';
	import AliasHistory from '#lib/AliasHistory.svelte';
	import AliasRequestForm from '#lib/AliasRequestForm.svelte';
	import MessageList from '#lib/MessageList.svelte';
	import RequestCard from '#lib/RequestCard.svelte';

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
		<h1 id="alias-name" style:--agency={alias.colour ?? 'transparent'}>{alias.name}</h1>
		<dl>
			<dt>{alias.pattern.includes('%') || alias.pattern.includes('_') ? 'Pattern' : 'Capcode'}</dt>
			<dd class="mono">{alias.pattern}</dd>
			<dt>Agency</dt>
			<dd>{alias.agency ?? 'None (text only)'}</dd>
			<dt>Icon</dt>
			<dd>{alias.icon ?? 'None'}</dd>
			<dt>Colour</dt>
			<dd>
				{#if alias.colour}<span class="swatch" style:background={alias.colour} aria-hidden="true"></span>{alias.colour}
				{:else}None{/if}
			</dd>
			<dt>Ignored</dt>
			<dd>{alias.ignored ? 'Yes: its pages are hidden from the main list' : 'No'}</dd>
		</dl>
		{#if !/[%_]/.test(alias.pattern)}
			<p><a href="/reports?capcode={alias.pattern}">Report for this capcode</a></p>
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
	<MessageList {query} base={{ alias_id: String(alias.id), ignored: 'true' }} level={2} />
{/if}

<style>
	.about {
		padding: 0 0.75rem 0.5rem;
		border-bottom: 1px solid var(--border);
	}
	h1 {
		margin: 0.75rem 0 0.5rem;
		border-left: 0.4rem solid var(--agency);
		padding-left: 0.4rem;
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
		overflow-wrap: anywhere;
	}
	.mono {
		font-family: var(--mono);
	}
	.swatch {
		display: inline-block;
		width: 0.9rem;
		height: 0.9rem;
		border: 1px solid var(--border);
		border-radius: 0.15rem;
		vertical-align: -0.1rem;
		margin-right: 0.4rem;
	}
	.asks {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: 0.5rem 0;
	}
</style>
