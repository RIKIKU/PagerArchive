<script lang="ts">
	import { page } from '$app/state';
	import { api, type AliasView } from '#lib/api.ts';
	import { criteria } from '#lib/filters.ts';
	import MessageList from '#lib/MessageList.svelte';

	// R35e: the alias, then the messages from the capcodes it covers. Read-only
	// in v1. Pages to an ignored alias are shown here: that's what was asked for.
	let alias: AliasView | undefined = $state();
	let error = $state('');
	const id = $derived(page.params.id ?? '');
	const query = $derived(criteria(page.url));

	$effect(() => {
		error = '';
		api
			.alias(id)
			.then((a) => (alias = a))
			.catch((e) => (error = e.message));
	});
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
		<p class="note">Aliases are changed by import or the command line.</p>
	</section>
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
	.note {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.5rem 0 0;
	}
</style>
