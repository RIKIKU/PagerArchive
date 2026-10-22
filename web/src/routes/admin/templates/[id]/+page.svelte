<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type AdminTemplate, type TemplateInput } from '#lib/api.ts';
	import AgencyIcon from '#lib/AgencyIcon.svelte';
	import TemplateForm from '#lib/TemplateForm.svelte';

	// R64: one template in Administration: edit it (at once, for every alias
	// using it), add, replace or remove its icon (R65), and delete it, which
	// the server refuses while aliases use it.
	let t: AdminTemplate | undefined = $state();
	let profiles: string[] = $state([]);
	let error = $state('');
	let notice = $state(page.url.searchParams.has('added') ? 'Template added. You can add its icon below.' : '');
	let iconError = $state('');
	let iconNotice = $state('');
	let file: FileList | undefined = $state();
	let confirming = $state(false);
	let busy = $state(false);
	let formKey = $state(0);
	const id = $derived(Number(page.params.id));

	function load() {
		return api
			.adminTemplates()
			.then((all) => {
				t = all.find((x) => x.id === id);
				if (!t) error = "That template doesn't exist any more.";
			})
			.catch((e) => (error = e.message));
	}
	$effect(() => {
		error = '';
		load();
	});
	api
		.agencyProfiles()
		.then((p) => (profiles = p))
		.catch(() => {});

	async function save(input: TemplateInput) {
		const reparse = input.agency_profile !== (t!.agency_profile ?? '');
		await api.editTemplate(t!.id, input);
		notice = `Saved. Every alias using it shows the change at once.${reparse ? ' Their pages are re-parsed with the new agency profile in the background.' : ''}`;
		await load();
		formKey++;
	}

	async function upload(ev: SubmitEvent) {
		ev.preventDefault();
		iconError = iconNotice = '';
		const f = file?.[0];
		if (!f) {
			iconError = 'Choose an image file.';
			return;
		}
		busy = true;
		try {
			await api.setIcon(t!.id, f);
			iconNotice = 'Icon saved. It shows beside every alias using this template.';
			(ev.target as HTMLFormElement).reset();
			file = undefined;
			await load();
		} catch (e) {
			iconError = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	async function removeIcon() {
		iconError = iconNotice = '';
		busy = true;
		try {
			await api.removeIcon(t!.id);
			iconNotice = 'Icon removed.';
			await load();
		} catch (e) {
			iconError = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	async function remove() {
		error = notice = '';
		busy = true;
		try {
			await api.deleteTemplate(t!.id);
			goto('/admin/templates?deleted=1', { replaceState: true });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
			confirming = false;
		}
	}
</script>

<svelte:head><title>{t?.name ?? 'Template'} · Templates · PagerArchive</title></svelte:head>

<h1>{t ? `Template “${t.name}”` : 'Template'}</h1>
<p class="links">
	<a href="/admin/templates">All templates</a>
	{#if t}· {t.aliases} alias{t.aliases === 1 ? '' : 'es'} use{t.aliases === 1 ? 's' : ''} it{/if}
</p>
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if t}
	{#key formKey}
		<TemplateForm
			heading="Edit"
			submitLabel="Save"
			initial={{ name: t.name, agency: t.agency, colour: t.colour ?? '', agency_profile: t.agency_profile ?? '' }}
			{profiles}
			onSave={save}
		/>
	{/key}

	<section class="icon" aria-labelledby="icon-h">
		<h2 id="icon-h">Icon</h2>
		<div class="current" data-testid="current-icon">
			{#if t.icon}
				<AgencyIcon src={t.icon} alt={t.agency} large />
				<span>Shown beside the name of every alias using this template.</span>
			{:else}
				<span class="none">No icon. Its aliases show their colour only.</span>
			{/if}
		</div>
		<form onsubmit={upload}>
			<label for="icon-file">{t.icon ? 'Replace it with' : 'Add an icon'}</label>
			<input id="icon-file" type="file" bind:files={file} accept=".png,.ico,.gif,.jpg,.jpeg,.webp,image/png,image/x-icon,image/gif,image/jpeg,image/webp"
				aria-describedby="icon-help" />
			<p class="help" id="icon-help">
				A PNG, ICO, GIF, JPEG or WebP file of at most 100 KB. It's stored as a 64×64 image, scaled to fit. SVG files aren't accepted.
			</p>
			<div aria-live="polite">
				{#if iconNotice}<p class="ok" role="status">{iconNotice}</p>{/if}
			</div>
			{#if iconError}<p class="error" role="alert" data-testid="icon-error">{iconError}</p>{/if}
			<div class="actions">
				<button type="submit" disabled={busy}>{t.icon ? 'Replace icon' : 'Upload icon'}</button>
				{#if t.icon}<button type="button" class="secondary" disabled={busy} onclick={removeIcon}>Remove icon</button>{/if}
			</div>
		</form>
	</section>

	<section class="danger-zone" aria-labelledby="delete-h">
		<h2 id="delete-h">Delete</h2>
		{#if confirming}
			<p data-testid="delete-confirm">Delete “{t.name}”? A template that aliases use can't be deleted.</p>
			<div class="actions">
				<button type="button" class="danger" disabled={busy} onclick={remove}>Delete template</button>
				<button type="button" class="secondary" onclick={() => (confirming = false)}>Don't</button>
			</div>
		{:else}
			<button type="button" class="secondary" onclick={() => ((confirming = true), (notice = ''), (error = ''))}>Delete…</button>
		{/if}
	</section>
{/if}

<style>
	.links {
		margin: 0 0.75rem;
		font-size: var(--text-sm);
	}
	.ok {
		margin: 0.5rem 0;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 1rem 0 0.25rem;
	}
	.icon,
	.danger-zone {
		margin: 0 0.75rem;
		max-width: 44rem;
	}
	.current {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin: 0.25rem 0 0.75rem;
	}
	.none {
		color: var(--muted);
	}
	label {
		display: block;
		margin-bottom: 0.2rem;
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
