<script lang="ts">
	// A request for a new alias, a change or a deletion (R54), or an edit of
	// one's own waiting request (R56). Users choose from existing templates.
	// The server checks everything again (R27a) and says what's wrong.
	import { untrack } from 'svelte';
	import { api, type AliasRequest, type RequestKind, type TemplateChoice } from '#lib/api.ts';

	interface Current {
		id: number;
		pattern: string;
		name: string;
		template_id: number | null;
		ignored: boolean;
	}

	let {
		kind,
		alias,
		pattern = '',
		existing,
		onDone,
		onCancel
	}: {
		kind: RequestKind;
		alias?: Current; // change and delete: the alias as it is now
		pattern?: string; // add: a capcode or pattern to start with
		existing?: AliasRequest; // editing this waiting request
		onDone: (message: string) => void;
		onCancel?: () => void;
	} = $props();

	const uid = $props.id();
	let templates: TemplateChoice[] = $state([]);
	api
		.templates()
		.then((t) => (templates = t))
		.catch(() => {});

	// Starting values: the request being edited, else the alias as it is.
	// Later prop changes don't reset the form.
	const start = untrack(() => ({ existing, alias, pattern }));
	const p = start.existing?.proposed;
	let pat = $state(start.existing?.pattern ?? start.pattern);
	let name = $state(p?.name ?? start.alias?.name ?? '');
	let template: string = $state(
		String((p?.set_template ? p.template_id : (start.alias?.template_id ?? p?.template_id)) ?? '')
	);
	let ignored = $state(p?.ignored ?? start.alias?.ignored ?? false);
	let note = $state(start.existing?.note ?? '');
	let error = $state('');
	let busy = $state(false);

	const heading = $derived(
		existing ? 'Edit your request' : kind === 'add' ? 'Request a new alias' : kind === 'delete' ? 'Request deletion' : 'Request a change'
	);

	async function submit(ev: SubmitEvent) {
		ev.preventDefault();
		error = '';
		busy = true;
		const tid = template === '' ? null : Number(template);
		const body =
			kind === 'delete'
				? { note }
				: kind === 'add'
					? { pattern: pat, name, template_id: tid, ignored, note }
					: { name, set_template: true, template_id: tid, ignored, note };
		try {
			if (existing) {
				await api.editRequest(existing.id, body);
				onDone('Your request was saved. It waits for an admin to review it.');
			} else {
				await api.submitRequest({ kind, alias_id: alias?.id, ...body });
				onDone('Your request was sent. An admin will review it; follow it in My requests.');
			}
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<form class="request-form" onsubmit={submit} aria-labelledby="{uid}-h" data-testid="request-form">
	<h2 id="{uid}-h">{heading}</h2>
	{#if kind === 'add'}
		<div class="field">
			<label for="{uid}-pattern">Capcode or pattern</label>
			<input id="{uid}-pattern" bind:value={pat} required maxlength="7" inputmode="numeric" autocomplete="off"
				readonly={!!existing} aria-describedby="{uid}-pattern-help" />
			<p class="help" id="{uid}-pattern-help">Up to 7 digits. A pattern uses % for any run of digits and _ for one digit.</p>
		</div>
	{/if}
	{#if kind !== 'delete'}
		<div class="field">
			<label for="{uid}-name">Name</label>
			<input id="{uid}-name" bind:value={name} required maxlength="100" autocomplete="off" />
		</div>
		<div class="field">
			<label for="{uid}-template">Template</label>
			<select id="{uid}-template" bind:value={template}>
				<option value="">None (text only)</option>
				{#each templates as t (t.id)}
					<option value={String(t.id)}>{t.name}{t.agency !== t.name ? ` (${t.agency})` : ''}</option>
				{/each}
			</select>
		</div>
		<label class="check"><input type="checkbox" bind:checked={ignored} /> Ignored: hide its pages from the main list</label>
	{:else}
		<p class="help">An admin reviews the request before the alias is deleted. Its pages stay; they show the capcode instead.</p>
	{/if}
	<div class="field">
		<label for="{uid}-note">Note for the admins (optional)</label>
		<textarea id="{uid}-note" bind:value={note} maxlength="500" rows="2"></textarea>
	</div>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	<div class="actions">
		<button type="submit" disabled={busy}>{existing ? 'Save request' : 'Send request'}</button>
		{#if onCancel}<button type="button" class="secondary" onclick={onCancel}>Cancel</button>{/if}
	</div>
</form>

<style>
	.request-form {
		margin: 0.75rem 0;
		padding: 0.75rem;
		border: 1px solid var(--border);
		border-radius: 0.4rem;
		background: var(--surface);
		max-width: 36rem;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 0.5rem;
	}
	.field {
		margin: 0.5rem 0;
	}
	.field label {
		display: block;
		margin-bottom: 0.2rem;
	}
	.check {
		display: inline-flex;
		align-items: baseline;
		gap: 0.4rem;
		margin: 0.25rem 0;
	}
	.help {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.25rem 0;
	}
	textarea {
		font: inherit;
		color: inherit;
		background: var(--bg);
		border: 1px solid var(--muted);
		border-radius: 0.3rem;
		padding: 0.35rem 0.5rem;
		width: 100%;
	}
	.error {
		padding: 0.25rem 0;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}
</style>
