<script lang="ts">
	import { api, type AliasRequest, type Meta } from '#lib/api.ts';
	import RequestCard from '#lib/RequestCard.svelte';
	import { refreshWaiting } from '#lib/waiting.svelte.ts';

	// R55: requests waiting for review, oldest first, each with the current
	// value beside the proposed one. Approve applies it exactly as submitted;
	// Reject needs feedback. Admins can't edit a request.
	let meta: Meta | undefined = $state();
	let tab: 'waiting' | 'closed' = $state('waiting');
	let requests: AliasRequest[] | undefined = $state();
	let error = $state('');
	let notice = $state('');
	let rejecting: number | undefined = $state();
	let feedback = $state('');
	let feedbackError = $state('');
	let busy = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.adminRequests(tab)
			.then((r) => (requests = r))
			.catch((e) => (error = e.message));
	}
	$effect(() => {
		requests = undefined;
		load();
	});

	function describe(r: AliasRequest) {
		const name = r.current?.name ?? r.alias_name;
		return r.kind === 'add' ? `the new alias for ${r.pattern}` : `the request for “${name}” (${r.pattern})`;
	}

	async function act(what: () => Promise<string>, keepRejecting = false) {
		error = notice = feedbackError = '';
		busy = true;
		try {
			notice = await what();
			rejecting = undefined;
			feedback = '';
			load();
			refreshWaiting();
		} catch (e) {
			if (keepRejecting) feedbackError = (e as Error).message;
			else error = (e as Error).message;
			load();
		} finally {
			busy = false;
		}
	}

	const approve = (r: AliasRequest) =>
		act(async () => {
			await api.approveRequest(r.id);
			return `Approved ${describe(r)}.`;
		});
	const reject = (r: AliasRequest) =>
		act(async () => {
			await api.rejectRequest(r.id, feedback);
			return `Rejected ${describe(r)}.`;
		}, true);
</script>

<svelte:head><title>Alias requests · PagerArchive</title></svelte:head>

<h1>Alias requests</h1>
<p class="status">
	Approving applies a request exactly as it was made: a change sets only what it asks for. Rejecting needs feedback for
	the person who asked. Requests can't be edited here; only the person who made one can change it.
</p>
<div class="tabs" role="group" aria-label="Show">
	<button type="button" class:secondary={tab !== 'waiting'} aria-pressed={tab === 'waiting'} onclick={() => ((tab = 'waiting'), (notice = ''))}>Waiting</button>
	<button type="button" class:secondary={tab !== 'closed'} aria-pressed={tab === 'closed'} onclick={() => ((tab = 'closed'), (notice = ''))}>Recently closed</button>
</div>
<div aria-live="polite">
	{#if notice}<p class="ok" role="status">{notice}</p>{/if}
</div>
{#if error}<p class="error" role="alert">{error}</p>{/if}

<div class="list" data-testid="review-list">
	{#if requests && requests.length === 0}
		<p class="status">{tab === 'waiting' ? 'Nothing is waiting for review.' : 'No closed requests yet.'}</p>
	{/if}
	{#each requests ?? [] as r (r.id)}
		<div id="request-{r.id}">
			<RequestCard {r} {tz} level={2}>
				{#snippet actions()}
					{#if r.status === 'waiting'}
						{#if rejecting === r.id}
							<form class="reject" onsubmit={(ev) => (ev.preventDefault(), reject(r))}>
								<label for="feedback-{r.id}">Feedback for the person who asked (required)</label>
								<textarea id="feedback-{r.id}" bind:value={feedback} maxlength="500" rows="2"
									aria-invalid={feedbackError ? 'true' : undefined} aria-describedby={feedbackError ? `feedback-err-${r.id}` : undefined}></textarea>
								{#if feedbackError}<p class="error" id="feedback-err-{r.id}" role="alert">{feedbackError}</p>{/if}
								<div class="buttons">
									<button type="submit" class="danger" disabled={busy}>Reject</button>
									<button type="button" class="secondary" onclick={() => ((rejecting = undefined), (feedbackError = ''))}>Cancel</button>
								</div>
							</form>
						{:else}
							<button type="button" disabled={busy || !!r.now_aliased_as} onclick={() => approve(r)} aria-label="Approve: {describe(r)}">Approve</button>
							<button type="button" class="secondary" disabled={busy} onclick={() => ((rejecting = r.id), (feedback = ''), (feedbackError = ''))} aria-label="Reject: {describe(r)}">Reject…</button>
						{/if}
					{/if}
				{/snippet}
			</RequestCard>
		</div>
	{/each}
</div>

<style>
	.tabs {
		display: flex;
		gap: 0.5rem;
		margin: 0 0.75rem 0.5rem;
	}
	.ok {
		margin: 0 0.75rem;
	}
	.list {
		margin: 0 0.75rem;
		max-width: 48rem;
	}
	.reject {
		width: 100%;
		max-width: 36rem;
	}
	.reject label {
		display: block;
		margin-bottom: 0.2rem;
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
	.reject .error {
		padding: 0.25rem 0;
	}
	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.4rem;
	}
</style>
