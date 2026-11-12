<script lang="ts">
	// R82: the Albums group's start page: the user's albums, and a new one.
	// Messages are added from a message's own view ("Add to album").
	import { goto } from '$app/navigation';
	import { api } from '#lib/api.ts';
	import { albums, refreshAlbums, retentionLine } from '#lib/albums.svelte.ts';
	import Icon from '#lib/Icon.svelte';

	const MAX_NAME = 100;
	const MAX_DESCRIPTION = 2000;

	let name = $state('');
	let description = $state('');
	let error = $state('');
	let busy = $state(false);

	refreshAlbums();

	async function create(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const a = await api.createAlbum(name, description);
			await refreshAlbums();
			goto(`/albums/${a.id}`);
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}
	const plural = (n: number) => `${n} message${n === 1 ? '' : 's'}`;
</script>

<svelte:head><title>Albums · PagerArchive</title></svelte:head>

<h1>Albums</h1>
<p class="lead">Messages you keep for later, each with a note if you like. Only you see your albums. Add a message from its own page, with "Add to album".</p>
<p class="muted">{retentionLine}</p>

{#if albums.loaded && albums.list.length === 0}
	<p data-testid="no-albums">You have no albums yet.</p>
{:else}
	<ul class="cards" data-testid="albums">
		{#each albums.list as a (a.id)}
			<li>
				<span class="icon-box"><Icon name="albums" /></span>
				<div>
					<a href="/albums/{a.id}">{a.name}</a>
					<p class="count">{plural(a.messages)}</p>
					{#if a.description}<p class="description">{a.description}</p>{/if}
				</div>
			</li>
		{/each}
	</ul>
{/if}

<section class="new" aria-labelledby="new-album">
	<h2 id="new-album">New album</h2>
	<form onsubmit={create}>
		<label for="album-name">Name</label>
		<input id="album-name" bind:value={name} required maxlength={MAX_NAME} autocomplete="off" />
		<label for="album-description">Description <span class="hint">(optional: what it's for, at most {MAX_DESCRIPTION} characters)</span></label>
		<textarea id="album-description" bind:value={description} maxlength={MAX_DESCRIPTION} rows="3"></textarea>
		<div><button type="submit" disabled={busy}><Icon name="add" />Create album</button></div>
		{#if error}<p class="error" role="alert">{error}</p>{/if}
	</form>
</section>

<style>
	.lead {
		max-width: var(--measure);
	}
	.muted,
	.hint {
		color: var(--muted);
	}
	.muted {
		max-width: var(--measure);
		font-size: var(--text-sm);
	}
	.hint {
		font-size: var(--text-xs);
	}
	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(var(--field-wide), 1fr));
		gap: var(--space-md);
		margin: var(--space-lg) 0;
		padding: 0;
		list-style: none;
	}
	li {
		position: relative;
		display: flex;
		align-items: flex-start;
		gap: var(--space-md);
		padding: var(--space-lg);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
	}
	li:hover {
		background: var(--hover);
	}
	li > div {
		min-width: 0;
	}
	.icon-box {
		display: inline-grid;
		place-items: center;
		flex: none;
		width: var(--target);
		height: var(--target);
		border-radius: var(--radius-button);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	li a {
		font-size: var(--text-lg);
		font-weight: var(--weight-strong);
		overflow-wrap: anywhere;
	}
	li a::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	li p {
		margin: var(--space-2xs) 0 0;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.description {
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
		overflow-wrap: anywhere;
	}
	.new {
		max-width: var(--measure-narrow);
		margin-top: var(--space-2xl);
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}
	form > div {
		margin-top: var(--space-sm);
	}
	.error {
		color: var(--danger-text);
	}
</style>
