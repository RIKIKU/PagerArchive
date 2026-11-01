<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type AdminTemplate, type TemplateInput } from '#lib/api.ts';
	import AgencyIcon from '#lib/AgencyIcon.svelte';
	import TemplateForm from '#lib/TemplateForm.svelte';

	// R64: every template with its icon, colour, agency profile and how many
	// aliases use it, and Add. Each one's page edits or deletes it.
	let rows: AdminTemplate[] | undefined = $state();
	let profiles: string[] = $state([]);
	let error = $state('');
	let adding = $state(false);
	const deleted = $derived(page.url.searchParams.has('deleted'));

	api
		.adminTemplates()
		.then((t) => (rows = t))
		.catch((e) => (error = e.message));
	api
		.agencyProfiles()
		.then((p) => (profiles = p))
		.catch(() => {});

	async function add(t: TemplateInput) {
		const r = await api.createTemplate(t);
		goto(`/admin/templates/${r.id}?added=1`);
	}
</script>

<svelte:head><title>Templates · PagerArchive</title></svelte:head>

<h1>Templates</h1>
<p class="status">
	A template gives its aliases an agency, an icon, a colour and the agency profile that parses their pages. Changes here take
	effect at once for every alias using it. Users choose from these templates when they ask for an alias.
</p>
{#if deleted}<p class="ok" role="status">Template deleted.</p>{/if}
<div class="tools">
	{#if !adding}<button type="button" onclick={() => (adding = true)}>Add template</button>{/if}
</div>
{#if adding}
	<TemplateForm
		heading="Add a template"
		submitLabel="Add template"
		initial={{ name: '', agency: '', colour: '', agency_profile: '' }}
		{profiles}
		onSave={add}
		onCancel={() => (adding = false)}
	/>
{/if}

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if rows}
	<p class="status" aria-live="polite">{rows.length === 0 ? 'No templates yet.' : `${rows.length} template${rows.length === 1 ? '' : 's'}.`}</p>
	{#if rows.length}
		<div class="scroll">
			<table data-testid="template-table">
				<thead>
					<tr>
						<th scope="col">Name</th>
						<th scope="col">Icon</th>
						<th scope="col">Agency</th>
						<th scope="col">Colour</th>
						<th scope="col">Agency profile</th>
						<th scope="col">Aliases</th>
					</tr>
				</thead>
				<tbody>
					{#each rows as t (t.id)}
						<tr data-template={t.name}>
							<td><a href="/admin/templates/{t.id}">{t.name}</a></td>
							<td>{#if t.icon}<AgencyIcon src={t.icon} alt={t.agency} />{:else}<span class="none">None</span>{/if}</td>
							<td>{t.agency}</td>
							<td>
								{#if t.colour}<span class="swatch" style:background={t.colour} aria-hidden="true"></span>{t.colour}
								{:else}<span class="none">None</span>{/if}
							</td>
							<td>
								{#if t.agency_profile}
									<span class="mono">{t.agency_profile}</span>
									{#if profiles.length && !profiles.includes(t.agency_profile)}<span class="tag warn">not loaded: text only</span>{/if}
								{:else}<span class="none">None (text only)</span>{/if}
							</td>
							<td class="count">{t.aliases}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/if}

<style>
	.ok {
		margin: 0 0 var(--space-sm);
	}
	.tools {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin: 0 0 var(--space-sm);
	}
	.scroll {
		margin: 0;
		overflow-x: auto;
	}
	table {
		border-collapse: collapse;
		width: 100%;
		font-size: var(--text-md);
	}
	th,
	td {
		text-align: left;
		padding: var(--space-xs) var(--space-md) var(--space-xs) 0;
		border-bottom: var(--border-width) solid var(--border);
		vertical-align: middle;
		overflow-wrap: anywhere;
	}
	th {
		color: var(--muted);
		font-weight: var(--weight-strong);
	}
	.count {
		font-variant-numeric: tabular-nums;
	}
	.none {
		color: var(--muted);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.swatch {
		display: inline-block;
		width: var(--swatch-size-small);
		height: var(--swatch-size-small);
		margin-right: var(--space-2xs);
		vertical-align: middle;
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-small);
	}
	.tag {
		font-size: var(--text-xs);
		border-radius: var(--radius-pill);
		padding: 0 var(--space-sm);
		margin-left: var(--space-2xs);
		white-space: nowrap;
	}
	.tag.warn {
		background: var(--warn-bg);
		color: var(--warn-fg);
	}
</style>
