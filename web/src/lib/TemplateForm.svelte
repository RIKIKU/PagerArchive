<script lang="ts">
	// An admin adds or edits a template (R64). Changes take effect at once for
	// every alias using it; the server checks the values and says what's wrong.
	import { untrack } from 'svelte';
	import type { TemplateInput } from '#lib/api.ts';

	let {
		initial,
		profiles,
		heading,
		submitLabel,
		onSave,
		onCancel
	}: {
		initial: TemplateInput;
		/** The agency profiles loaded at start-up. */
		profiles: string[];
		heading: string;
		submitLabel: string;
		onSave: (t: TemplateInput) => Promise<void>;
		onCancel?: () => void;
	} = $props();

	const uid = $props.id();
	// The form starts from the values given; later prop changes don't reset it.
	const start = untrack(() => initial);
	let name = $state(start.name);
	let agency = $state(start.agency);
	let colour = $state(start.colour);
	let profile = $state(start.agency_profile);
	let error = $state('');
	let busy = $state(false);
	// A profile that's no longer loaded stays choosable, so saving other
	// changes doesn't drop it silently.
	const missing = $derived(start.agency_profile !== '' && !profiles.includes(start.agency_profile));

	async function submit(ev: SubmitEvent) {
		ev.preventDefault();
		error = '';
		busy = true;
		try {
			await onSave({ name, agency, colour, agency_profile: profile });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<form class="template-form" onsubmit={submit} aria-labelledby="{uid}-h" data-testid="template-form">
	<h2 id="{uid}-h">{heading}</h2>
	<div class="grid">
		<div class="field">
			<label for="{uid}-name">Name</label>
			<input id="{uid}-name" bind:value={name} required maxlength="100" autocomplete="off" />
		</div>
		<div class="field">
			<label for="{uid}-agency">Agency</label>
			<input id="{uid}-agency" bind:value={agency} required maxlength="100" autocomplete="off" aria-describedby="{uid}-agency-help" />
		</div>
	</div>
	<p class="help" id="{uid}-agency-help">The agency's name is shown with its pages, and is its icon's text alternative.</p>
	<div class="grid">
		<div class="field">
			<label for="{uid}-colour">Colour</label>
			<div class="colour">
				<input id="{uid}-colour" bind:value={colour} maxlength="32" autocomplete="off" aria-describedby="{uid}-colour-help" />
				<span class="swatch" style:background={colour || 'transparent'} aria-hidden="true"></span>
			</div>
			<p class="help" id="{uid}-colour-help">A name like red or a hex value like #b3261e. Leave it empty for none.</p>
		</div>
		<div class="field">
			<label for="{uid}-profile">Agency profile</label>
			<select id="{uid}-profile" bind:value={profile} aria-describedby="{uid}-profile-help">
				<option value="">None (text only)</option>
				{#each profiles as p (p)}<option value={p}>{p}</option>{/each}
				{#if missing}<option value={start.agency_profile}>{start.agency_profile} (not loaded)</option>{/if}
			</select>
			<p class="help" id="{uid}-profile-help">Parses its aliases' pages. Changing it re-parses them in the background.</p>
		</div>
	</div>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	<div class="actions">
		<button type="submit" disabled={busy}>{submitLabel}</button>
		{#if onCancel}<button type="button" class="secondary" onclick={onCancel}>Cancel</button>{/if}
	</div>
</form>

<style>
	.template-form {
		margin: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--border);
		border-radius: 0.4rem;
		background: var(--surface);
		max-width: 44rem;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 0.5rem;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
		gap: 0 0.75rem;
	}
	.field {
		margin: 0.5rem 0;
	}
	.field label {
		display: block;
		margin-bottom: 0.2rem;
	}
	.colour {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.swatch {
		flex: none;
		width: 1.5rem;
		height: 1.5rem;
		border: 1px solid var(--border);
		border-radius: 0.2rem;
	}
	.help {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.2rem 0 0.25rem;
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
