<script lang="ts">
	// An admin adds or edits an alias (R53). Changes take effect at once; the
	// server applies R27a and says what's wrong.
	import { untrack } from 'svelte';
	import { api, type AliasInput, type TemplateChoice } from '#lib/api.ts';

	let {
		initial,
		heading,
		submitLabel,
		onSave,
		onCancel
	}: {
		initial: AliasInput;
		heading: string;
		submitLabel: string;
		onSave: (a: AliasInput) => Promise<void>;
		onCancel?: () => void;
	} = $props();

	const uid = $props.id();
	let templates: TemplateChoice[] = $state([]);
	api
		.templates()
		.then((t) => (templates = t))
		.catch(() => {});

	// The form starts from the values given; later prop changes don't reset it.
	const start = untrack(() => initial);
	let pattern = $state(start.pattern);
	let name = $state(start.name);
	let template: string = $state(start.template_id === null ? '' : String(start.template_id));
	let ignored = $state(start.ignored);
	let error = $state('');
	let busy = $state(false);

	async function submit(ev: SubmitEvent) {
		ev.preventDefault();
		error = '';
		busy = true;
		try {
			await onSave({ pattern, name, template_id: template === '' ? null : Number(template), ignored });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<form class="alias-form" onsubmit={submit} aria-labelledby="{uid}-h" data-testid="alias-form">
	<h2 id="{uid}-h">{heading}</h2>
	<div class="grid">
		<div class="field">
			<label for="{uid}-pattern">Capcode or pattern</label>
			<input id="{uid}-pattern" bind:value={pattern} required maxlength="7" inputmode="numeric" autocomplete="off"
				aria-describedby="{uid}-pattern-help" />
		</div>
		<div class="field">
			<label for="{uid}-name">Name</label>
			<input id="{uid}-name" bind:value={name} required maxlength="100" autocomplete="off" />
		</div>
	</div>
	<p class="help" id="{uid}-pattern-help">Up to 7 digits. A pattern uses % for any run of digits and _ for one digit.</p>
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
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	<div class="actions">
		<button type="submit" disabled={busy}>{submitLabel}</button>
		{#if onCancel}<button type="button" class="secondary" onclick={onCancel}>Cancel</button>{/if}
	</div>
</form>

<style>
	.alias-form {
		margin: var(--space-md) 0;
		padding: var(--space-md);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-badge);
		background: var(--surface);
		max-width: var(--measure);
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 var(--space-sm);
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(var(--card-min), 1fr));
		gap: 0 var(--space-md);
	}
	.field {
		margin: var(--space-sm) 0;
	}
	.field label {
		display: block;
		margin-bottom: var(--space-2xs);
	}
	.check {
		display: inline-flex;
		align-items: baseline;
		gap: var(--space-xs);
		margin: var(--space-2xs) 0;
	}
	.help {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0 0 var(--space-2xs);
	}
	.error {
		padding: var(--space-2xs) 0;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
	}
</style>
