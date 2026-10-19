<script lang="ts">
	// R42b: deactivating a user and their invite tree starts with a preview.
	// Everyone who will be deactivated is ticked; the admin can untick anyone,
	// or spare a whole branch, and give a reason. If the tree changed since
	// the preview, confirming is refused and the updated preview shown, with
	// the admin's ticks kept for anyone unchanged (ADR 0008 §5).
	import { page } from '$app/state';
	import { SvelteMap, SvelteSet } from 'svelte/reactivity';
	import { api, ApiError, type DeactivationPreview, type Meta, type TreeDeactivateResult, type TreeNode } from '#lib/api.ts';
	import InviteTree, { everyone, type Ticks } from '#lib/InviteTree.svelte';

	let meta: Meta | undefined = $state();
	let preview: DeactivationPreview | undefined = $state();
	let reason = $state('');
	let error = $state('');
	let busy = $state(false);
	let done: TreeDeactivateResult | undefined = $state();
	const id = $derived(page.params.id ?? '');
	const tz = $derived(meta?.time_zone ?? 'UTC');

	const ticked = new SvelteSet<number>();
	const marks = new SvelteMap<number, 'new' | 'changed'>();

	api.meta().then((m) => (meta = m));
	$effect(() => {
		api
			.deactivationPreview(id)
			.then((p) => apply(p))
			.catch((e) => (error = e.message));
	});

	// apply shows a preview. Given the one it replaces, people who are new or
	// whose status, role or trusted mark changed start as the server says
	// and are marked; everyone else keeps the admin's tick.
	function apply(p: DeactivationPreview, old?: DeactivationPreview) {
		const before = new Map(old ? everyone(old.root).map((n) => [n.id, n]) : []);
		const keep = new Set(ticked);
		ticked.clear();
		marks.clear();
		for (const n of everyone(p.root)) {
			const o = before.get(n.id);
			if (old && !o) marks.set(n.id, 'new');
			else if (o && (o.status !== n.status || o.role !== n.role || o.trusted !== n.trusted)) marks.set(n.id, 'changed');
			const tick = old && o && !marks.has(n.id) ? keep.has(n.id) : n.tick?.start;
			if (tick && n.tick?.can) ticked.add(n.id);
		}
		preview = p;
	}

	const ticks: Ticks = {
		ticked,
		marks,
		toggle: (id) => (ticked.has(id) ? ticked.delete(id) : ticked.add(id)),
		spare: (n: TreeNode) => everyone(n).forEach((p) => ticked.delete(p.id))
	};

	const people = $derived(preview ? everyone(preview.root).filter((n) => ticked.has(n.id)) : []);
	const invitations = $derived(people.reduce((sum, n) => sum + n.invitations, 0));
	const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
	const totals = $derived(
		`${plural(people.length, 'person', 'people')} will be deactivated and ${plural(invitations, 'unused invitation', 'unused invitations')} cancelled`
	);

	async function confirm() {
		if (!preview) return;
		error = '';
		busy = true;
		try {
			done = await api.deactivateTree(preview.root.id, preview.version, [...ticked], reason.trim());
		} catch (e) {
			error = (e as Error).message;
			const body = e instanceof ApiError ? e.body : undefined;
			if (body?.code === 'tree_changed' && body.preview) apply(body.preview as DeactivationPreview, preview);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Deactivate invite tree · Administration · PagerArchive</title></svelte:head>

<h1>Deactivate {preview ? preview.root.label : 'user'} and their invite tree</h1>
{#if error}<p class="error" role="alert">{error}</p>{/if}

{#if done}
	<p class="ok" role="status">
		Done: {plural(done.deactivated, 'person', 'people')} deactivated, {plural(done.spared, 'person', 'people')} spared and {plural(
			done.invitations_cancelled,
			'unused invitation',
			'unused invitations'
		)} cancelled. They were signed out at once.
	</p>
	<p class="note"><a href="/admin/users/{id}">Back to {preview?.root.label}</a>, where this is recorded.</p>
{:else if preview}
	<p class="note">
		Everyone ticked will be signed out at once and their unused invitations cancelled. Untick anyone to spare them, or
		spare a whole branch. Trusted people start unticked. Spared people keep their invitations, and the record shows
		who invited them.
	</p>
	<InviteTree root={preview.root} {tz} {ticks} />
	<form
		onsubmit={(e) => {
			e.preventDefault();
			confirm();
		}}
	>
		<label for="reason">Reason (optional, only admins see it)</label>
		<textarea id="reason" rows="2" maxlength="500" bind:value={reason}></textarea>
		<p class="totals" aria-live="polite" data-testid="totals">{totals}.</p>
		<div class="actions">
			<button type="submit" class="danger" disabled={busy || people.length === 0}>Deactivate {plural(people.length, 'person', 'people')}</button>
			<a class="button secondary" href="/admin/users/{id}">Cancel</a>
		</div>
	</form>
{/if}

<style>
	.note {
		margin: 0.5rem 0.75rem;
		color: var(--muted);
		max-width: 44rem;
	}
	.ok,
	.error {
		margin: 0 0.75rem 0.5rem;
	}
	form {
		margin: 1rem 0.75rem;
		max-width: 44rem;
	}
	label {
		display: block;
		font-weight: 600;
		margin-bottom: 0.25rem;
	}
	textarea {
		width: 100%;
		font: inherit;
	}
	.totals {
		font-weight: 600;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
</style>
