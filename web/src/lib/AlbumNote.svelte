<script lang="ts">
	// R82f: a message's note in an album (why it was kept), and taking the
	// message out of the album (R82). The note is the owner's own words, plain
	// text, at most 500 characters; the same message in another album has its
	// own.
	import { api, type Message } from './api.ts';
	import { refreshAlbums } from './albums.svelte.ts';
	import Icon from './Icon.svelte';

	const MAX_NOTE = 500;

	let { m, album, onremoved }: { m: Message; album: number; onremoved?: (id: number) => void } = $props();

	let note = $state(m.note ?? '');
	let editing = $state(false);
	let draft = $state('');
	let error = $state('');
	let busy = $state(false);
	const uid = $props.id();

	function edit() {
		draft = note;
		error = '';
		editing = true;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const r = await api.setNote(album, m.id, draft);
			note = r.note ?? '';
			editing = false;
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}

	async function remove() {
		busy = true;
		error = '';
		try {
			await api.removeFromAlbum(album, m.id);
			refreshAlbums();
			onremoved?.(m.id);
		} catch (err) {
			error = (err as Error).message;
			busy = false;
		}
	}
</script>

<!-- Raised above the row's whole-row link, as the alias and job links are. -->
<div class="note-box">
	{#if editing}
		<form onsubmit={save}>
			<label for="{uid}-note">Note on this message <span class="hint">(only you see it; at most {MAX_NOTE} characters)</span></label>
			<textarea id="{uid}-note" bind:value={draft} maxlength={MAX_NOTE} rows="3"></textarea>
			<div class="actions">
				<button type="submit" disabled={busy}>Save note</button>
				<button type="button" class="secondary" onclick={() => (editing = false)}>Cancel</button>
				<span class="hint" aria-live="polite">{draft.length} of {MAX_NOTE}</span>
			</div>
		</form>
	{:else}
		{#if note}<p class="note" data-testid="album-note"><Icon name="note" /><span>{note}</span></p>{/if}
		<div class="actions">
			<button type="button" class="ghost" onclick={edit}
				><Icon name={note ? 'edit' : 'note'} />{note ? 'Edit note' : 'Add note'}</button
			>
			<button type="button" class="ghost" onclick={remove} disabled={busy}><Icon name="remove" />Take out</button>
		</div>
	{/if}
	{#if error}<p class="error" role="alert">{error}</p>{/if}
</div>

<style>
	.note-box {
		position: relative;
		z-index: var(--layer-content);
		margin-top: var(--space-sm);
	}
	.note {
		display: flex;
		align-items: flex-start;
		gap: var(--space-xs);
		margin: 0;
		padding: var(--space-xs) var(--space-sm);
		border-left: var(--rule-width) solid var(--accent);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
		border-radius: var(--radius-small);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-xs);
		margin-top: var(--space-2xs);
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-2xs);
		max-width: var(--measure);
	}
	.hint {
		color: var(--muted);
		font-size: var(--text-xs);
	}
	.error {
		margin: var(--space-2xs) 0 0;
		color: var(--danger-text);
	}
</style>
