<script lang="ts">
	import { api } from '#lib/api.ts';
	import FieldError from '#lib/FieldError.svelte';
	import FormSummary from '#lib/FormSummary.svelte';
	import { FormCheck } from '#lib/formCheck.svelte.ts';

	// R82g: the number says it's required, and is checked on save.
	const fc = new FormCheck();

	// R87: how many Emergency pages one job must have, to aliased capcodes, to
	// open a large event. Each stored page counts once, however many receivers
	// heard it. The default is 20. Events already recorded stay as they are;
	// new pages are judged by the new number.
	const MIN = 2;
	const MAX = 1000;
	let current: number | undefined = $state();
	let pages = $state(0);
	let error = $state('');
	let saved = $state('');
	let busy = $state(false);

	api
		.largeEventSettings()
		.then((s) => {
			current = pages = s.job_pages;
		})
		.catch((e) => (error = e.message));

	async function save(e: SubmitEvent) {
		e.preventDefault();
		error = saved = '';
		if (!fc.check(e.currentTarget as HTMLFormElement)) return;
		busy = true;
		try {
			const s = await api.setLargeEventSettings(pages);
			current = pages = s.job_pages;
			saved = `Saved: a job opens a large event when it has ${s.job_pages} Emergency pages.`;
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Large event settings · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Large event settings</h1>
	<p class="note">
		Admins only. A large event opens when an Emergency page carries one of the network's large-event codes (the network profile's
		pattern), or when one job has this many Emergency pages to capcodes with an alias. A page heard by several receivers counts once.
	</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if current !== undefined}
		<FormSummary check={fc} />
		<form novalidate onsubmit={save} oninput={fc.cleared}>
			<label
				><span>Emergency pages on one job that open a large event <span class="required">required</span></span>
				<input id="job-pages" type="number" bind:value={pages} min={MIN} max={MAX} step="1" required disabled={busy} aria-invalid={fc.invalid('job-pages')} aria-describedby={fc.describedBy('job-pages', 'job-pages-hint')} />
			</label>
			<button type="submit" disabled={busy || pages === current}>Save</button>
		</form>
		<FieldError check={fc} id="job-pages" />
		<p class="hint" id="job-pages-hint">Between {MIN} and {MAX}. The default is 20. Events already recorded stay as they are; pages from now on are judged by the new number.</p>
		<p role="status" class="saved">{saved}</p>
	{/if}
</div>

<style>
	.page {
		padding: 0 0 var(--space-lg);
		max-width: var(--measure-wide);
	}
	h1 {
		font-size: var(--text-xl);
		margin: var(--space-md) 0 var(--space-2xs);
	}
	.note,
	.hint {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	form {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-sm) var(--space-md);
	}
	label {
		display: flex;
		flex-direction: column;
		gap: var(--space-3xs);
		font-size: var(--text-sm);
	}
	input {
		width: var(--field-narrow);
	}
	.error {
		color: var(--danger-text);
	}
</style>
