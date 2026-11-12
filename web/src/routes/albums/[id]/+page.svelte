<script lang="ts">
	// R82, R82f: one album: its description at the top, then its messages,
	// newest first, as they are now (current aliases and redaction), each with
	// its note, within the search box's search and with the scrubber (R81b).
	// Renaming, the description and deleting it are here; adding is from a
	// message's own view. R82a: a line says what retention does to albums.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Album, type Meta } from '#lib/api.ts';
	import { refreshAlbums, retentionLine } from '#lib/albums.svelte.ts';
	import { date } from '#lib/format.ts';
	import { setTitle } from '#lib/here.svelte.ts';
	import Icon from '#lib/Icon.svelte';
	import MessageList from '#lib/MessageList.svelte';
	import { criteria } from '#lib/search.ts';

	const MAX_NAME = 100;
	const MAX_DESCRIPTION = 2000;

	let meta: Meta | undefined = $state();
	let album: Album | undefined = $state();
	let error = $state('');
	let editing = $state(false);
	let confirming = $state(false);
	let name = $state('');
	let description = $state('');
	let formError = $state('');
	let busy = $state(false);
	const id = $derived(page.params.id ?? '');
	const query = $derived(criteria(page.url));

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.album(id)
			.then((a) => {
				album = a;
				error = '';
			})
			.catch((e) => {
				album = undefined;
				error = e.status === 404 ? 'There’s no such album, or it isn’t yours.' : e.message;
			});
	}
	$effect(() => {
		void id;
		editing = confirming = false;
		load();
	});
	$effect(() => {
		if (album) setTitle(album.name);
	});

	function edit() {
		if (!album) return;
		name = album.name;
		description = album.description ?? '';
		formError = '';
		editing = true;
	}
	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!album) return;
		busy = true;
		formError = '';
		try {
			album = await api.updateAlbum(album.id, { name, description });
			editing = false;
			refreshAlbums();
		} catch (err) {
			formError = (err as Error).message;
		} finally {
			busy = false;
		}
	}
	async function remove() {
		if (!album) return;
		busy = true;
		try {
			await api.deleteAlbum(album.id);
			await refreshAlbums();
			goto('/albums');
		} catch (err) {
			formError = (err as Error).message;
			busy = false;
		}
	}
	function removed() {
		load();
		refreshAlbums();
	}
	const plural = (n: number) => `${n} message${n === 1 ? '' : 's'}`;
</script>

<svelte:head><title>{album?.name ?? 'Album'} · PagerArchive</title></svelte:head>

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if album}
	<section class="about" aria-labelledby="album-name">
		{#if editing}
			<form onsubmit={save}>
				<h1 id="album-name">Edit album</h1>
				<label for="album-name-field">Name</label>
				<input id="album-name-field" bind:value={name} required maxlength={MAX_NAME} autocomplete="off" />
				<label for="album-description">Description <span class="hint">(optional: what it's for, at most {MAX_DESCRIPTION} characters)</span></label>
				<textarea id="album-description" bind:value={description} maxlength={MAX_DESCRIPTION} rows="5"></textarea>
				<div class="actions">
					<button type="submit" disabled={busy}>Save</button>
					<button type="button" class="secondary" onclick={() => (editing = false)}>Cancel</button>
				</div>
				{#if formError}<p class="error" role="alert">{formError}</p>{/if}
			</form>
		{:else}
			<div class="title">
				<span class="icon-box"><Icon name="albums" /></span>
				<h1 id="album-name">{album.name}</h1>
			</div>
			<p class="count" data-testid="album-count">{plural(album.messages)}</p>
			{#if album.description}<p class="description" data-testid="album-description">{album.description}</p>{/if}
			{#if album.messages === 0 && album.emptied_at && meta}
				<p class="notice" data-testid="album-emptied">
					Its messages have passed the retention period: the last of them were deleted on {date(album.emptied_at, meta.time_zone)}.
				</p>
			{/if}
			<p class="muted">{retentionLine}</p>
			{#if confirming}
				<div class="confirm" role="group" aria-labelledby="confirm-delete">
					<p id="confirm-delete">Delete “{album.name}”? Its description and notes go with it; the messages stay in the archive.</p>
					<div class="actions">
						<button type="button" class="danger" onclick={remove} disabled={busy}><Icon name="remove" />Delete album</button>
						<button type="button" class="secondary" onclick={() => (confirming = false)}>Cancel</button>
					</div>
				</div>
			{:else}
				<div class="actions">
					<button type="button" class="secondary" onclick={edit}><Icon name="edit" />Rename or describe</button>
					<button type="button" class="secondary" onclick={() => (confirming = true)}><Icon name="remove" />Delete album</button>
				</div>
			{/if}
			{#if formError}<p class="error" role="alert">{formError}</p>{/if}
		{/if}
	</section>
	<div class="listbox">
		<MessageList {query} base={{ album: String(album.id), ignored: 'true' }} level={2} live={false} album={album.id} onremoved={removed} />
	</div>
{/if}

<style>
	.about {
		max-width: var(--measure);
	}
	.title {
		display: flex;
		align-items: center;
		gap: var(--space-md);
	}
	h1 {
		margin: 0;
		overflow-wrap: anywhere;
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
	.count,
	.muted,
	.hint {
		color: var(--muted);
	}
	.muted {
		font-size: var(--text-sm);
	}
	.hint {
		font-size: var(--text-xs);
	}
	.description {
		margin: var(--space-md) 0;
		padding: var(--space-md) var(--space-lg);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.notice {
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-small);
		background: var(--warn-bg);
		color: var(--warn-fg);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin: var(--space-sm) 0;
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}
	.error {
		color: var(--danger-text);
	}
	.listbox {
		margin-top: var(--space-xl);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		overflow: clip;
	}
</style>
