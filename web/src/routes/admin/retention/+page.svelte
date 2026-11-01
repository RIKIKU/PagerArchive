<script lang="ts">
	import { api, type Meta, type Retention, type RetentionConfirm } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	// R45, R46: how long messages are kept, in days. Shortening it asks the
	// admin to confirm the cut-off date; deletion happens at the next nightly
	// run. No row count is shown.
	let meta: Meta | undefined = $state();
	let r: Retention | undefined = $state();
	let days = $state(0);
	let confirm: RetentionConfirm | undefined = $state();
	let error = $state('');
	let saved = $state('');
	let busy = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	api
		.retention()
		.then((x) => {
			r = x;
			days = x.days;
		})
		.catch((e) => (error = e.message));

	async function save(confirmCutoff?: string) {
		busy = true;
		error = saved = '';
		try {
			const res = await api.setRetention(days, confirmCutoff);
			if ('code' in res) {
				confirm = res;
			} else {
				r = res;
				confirm = undefined;
				saved = `Saved: messages are kept for ${res.days.toLocaleString('en-AU')} days.`;
			}
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	function long(d: string): string {
		const [y, m, day] = d.split('-').map(Number);
		return new Intl.DateTimeFormat('en-AU', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' }).format(
			new Date(Date.UTC(y, m - 1, day))
		);
	}
</script>

<svelte:head><title>Retention · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Retention</h1>
	<p class="note">Admins only. Messages older than the retention period are deleted completely by the nightly run.</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if r}
		<form
			onsubmit={(e) => {
				e.preventDefault();
				confirm = undefined;
				save();
			}}
		>
			<label
				>Keep messages for (days)
				<input type="number" bind:value={days} min={r.min_days} max={r.max_days} step="1" required disabled={busy || !!confirm} />
			</label>
			<button type="submit" disabled={busy || !!confirm || days === r.days}>Save</button>
		</form>
		<p class="hint">Between {r.min_days} and {r.max_days.toLocaleString('en-AU')} days. The default is {r.default_days.toLocaleString('en-AU')} (about 10 years).</p>

		{#if confirm}
			<div class="confirm" role="alertdialog" aria-labelledby="confirm-msg">
				<p id="confirm-msg">{confirm.message}</p>
				<div class="actions">
					<button type="button" class="danger" onclick={() => save(confirm!.cutoff)} disabled={busy}>Confirm</button>
					<button type="button" onclick={() => (confirm = undefined)} disabled={busy}>Cancel</button>
				</div>
			</div>
		{/if}
		<p role="status" class="saved">{saved}</p>

		<h2>Nightly run</h2>
		<dl>
			<dt>Next run</dt>
			<dd><time datetime={isoAttr(r.next_run)}>{dateTime(r.next_run, tz)}</time></dd>
			<dt>It will delete</dt>
			<dd>Messages older than {long(r.cutoff)}</dd>
			<dt>Last run</dt>
			<dd>
				{#if r.last_run}
					<time datetime={isoAttr(r.last_run.at)}>{dateTime(r.last_run.at, tz)}</time>:
					{#if r.last_run.ok}{r.last_run.deleted.toLocaleString('en-AU')} deleted, {r.last_run.redacted.toLocaleString('en-AU')} redacted again
					{:else}<span class="warn">failed ({r.last_run.error})</span>{/if}
				{:else}Not yet{/if}
			</dd>
		</dl>
		<p class="note">Deleted messages may survive in your own database backups.</p>
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
	h2 {
		font-size: var(--text-base);
		margin-top: var(--space-2xl);
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
	.confirm {
		border: var(--border-width) solid var(--border);
		background: var(--warn-bg);
		color: var(--warn-fg);
		padding: var(--space-md);
		border-radius: var(--radius-small);
	}
	.confirm p {
		margin: 0 0 var(--space-sm);
		font-weight: var(--weight-strong);
	}
	.actions {
		display: flex;
		gap: var(--space-md);
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--space-2xs) var(--space-lg);
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
	}
	.warn {
		color: var(--warn-fg);
		background: var(--warn-bg);
	}
</style>
