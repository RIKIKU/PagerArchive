<script lang="ts">
	// R82: "Add to album", in a message's detail view. A dialog lists the
	// user's albums, a tick for each that holds the message; ticking adds it
	// and unticking takes it out, at once. A new album can be made there and
	// the message added to it. R77a: the Albums icon, as in the menu.
	import { api } from './api.ts';
	import { albums, refreshAlbums } from './albums.svelte.ts';
	import Icon from './Icon.svelte';

	let { message }: { message: number } = $props();

	let dialog: HTMLDialogElement;
	let holding = $state(new Set<number>());
	let name = $state('');
	let error = $state('');
	let status = $state('');
	let busy = $state(false);
	const uid = $props.id();

	function load() {
		api
			.messageAlbums(message)
			.then((r) => (holding = new Set(r.albums)))
			.catch(() => {});
	}
	$effect(() => {
		void message;
		load();
	});

	function open() {
		error = status = '';
		load();
		refreshAlbums();
		dialog.showModal();
	}

	async function toggle(id: number, on: boolean, albumName: string) {
		error = '';
		try {
			if (on) await api.addToAlbum(id, message);
			else await api.removeFromAlbum(id, message);
			const next = new Set(holding);
			if (on) next.add(id);
			else next.delete(id);
			holding = next;
			status = on ? `Added to ${albumName}.` : `Taken out of ${albumName}.`;
			refreshAlbums();
		} catch (err) {
			error = (err as Error).message;
		}
	}

	async function create(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const a = await api.createAlbum(name, '');
			await api.addToAlbum(a.id, message);
			holding = new Set([...holding, a.id]);
			status = `Added to ${a.name}.`;
			name = '';
			await refreshAlbums();
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}

	const inAny = $derived(holding.size > 0);
</script>

<span class="add">
	<button type="button" class="secondary" aria-haspopup="dialog" onclick={open}><Icon name="albums" />Add to album</button>
	{#if inAny}<span class="in" data-testid="in-albums">In {holding.size === 1 ? "1 album" : `${holding.size} albums`}</span>{/if}
</span>

<dialog bind:this={dialog} aria-labelledby="{uid}-title">
	<div class="head">
		<h2 id="{uid}-title">Add to album</h2>
		<button class="ghost" type="button" onclick={() => dialog.close()}><Icon name="close" />Close</button>
	</div>
	{#if albums.list.length > 0}
		<fieldset>
			<legend>Your albums</legend>
			<ul>
				{#each albums.list as a (a.id)}
					<li>
						<label
							><input type="checkbox" checked={holding.has(a.id)} onchange={(e) => toggle(a.id, e.currentTarget.checked, a.name)} />{a.name}
							<span class="count">({a.messages})</span></label
						>
					</li>
				{/each}
			</ul>
		</fieldset>
	{:else}
		<p>You have no albums yet: make one here.</p>
	{/if}
	<form onsubmit={create}>
		<label for="{uid}-name">New album</label>
		<div class="row">
			<input id="{uid}-name" bind:value={name} required maxlength="100" autocomplete="off" />
			<button type="submit" disabled={busy}><Icon name="add" />Create and add</button>
		</div>
	</form>
	<p class="status" role="status">{status}</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
</dialog>

<style>
	dialog {
		width: var(--dialog-width);
		max-width: var(--panel-max);
		padding: var(--space-lg);
		background: var(--surface-2);
		color: var(--fg);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
	}
	dialog::backdrop {
		background: var(--scrim);
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
	}
	h2 {
		margin: 0;
	}
	fieldset {
		margin: var(--space-md) 0;
		padding: 0;
		border: 0;
	}
	legend {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	ul {
		margin: var(--space-2xs) 0 0;
		padding: 0;
		list-style: none;
		max-height: var(--list-height);
		overflow-y: auto;
	}
	li label {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		min-height: var(--target);
		overflow-wrap: anywhere;
	}
	.count {
		color: var(--muted);
	}
	.row {
		display: flex;
		gap: var(--space-sm);
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-2xs);
	}
	.add {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-sm);
	}
	.in,
	.status {
		color: var(--muted);
	}
	.in {
		font-size: var(--text-sm);
	}
	.error {
		color: var(--danger-text);
	}
</style>
