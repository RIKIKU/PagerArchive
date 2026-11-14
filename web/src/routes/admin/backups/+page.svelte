<script lang="ts">
	// R86a: whether the nightly backups are working, and Back up now: the same
	// backup and check, asked for here and done by the backup service within a
	// minute. Restoring one is done on the server (docs/deploy.md, "Backups").
	import { api, type BackupStatus, type Meta } from '#lib/api.ts';
	import BackupAlert from '#lib/BackupAlert.svelte';
	import { dateTime, isoAttr } from '#lib/format.ts';

	// How often the page asks while a backup is asked for or running, and how
	// long a request may wait before the page says the service may be stopped.
	const POLL_MS = 3000;
	const STUCK_MS = 3 * 60 * 1000;

	let meta: Meta | undefined = $state();
	let b: BackupStatus | undefined = $state();
	let error = $state('');
	let busy = $state(false);
	// Set once this page has seen a backup asked for or running, so it can
	// say how that one went when it's done.
	let watching = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');
	api.meta().then((m) => (meta = m));
	function refresh() {
		api
			.backup()
			.then((x) => {
				if (x.run === 'queued' || x.run === 'running') watching = true;
				b = x;
				error = '';
			})
			.catch((e) => (error = e.message));
	}
	refresh();
	$effect(() => {
		if (b?.run !== 'queued' && b?.run !== 'running') return;
		const t = setTimeout(refresh, POLL_MS);
		return () => clearTimeout(t);
	});

	async function backUpNow() {
		busy = true;
		error = '';
		try {
			b = await api.backUpNow();
			watching = true;
		} catch (e) {
			error = (e as Error).message;
			refresh();
		} finally {
			busy = false;
		}
	}

	const summary: Record<BackupStatus['state'], string> = {
		ok: 'Working.',
		waiting: 'No backup yet: the first is tonight.',
		failed: 'The last backup failed.',
		overdue: 'Overdue.',
		off: 'This server has no backup service.'
	};
	const stuck = $derived(b?.run === 'queued' && !!b.requested_at && Date.now() - Date.parse(b.requested_at) > STUCK_MS);
</script>

<svelte:head><title>Backups · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Backups</h1>
	<p class="note">Admins only. Each night both databases are dumped to the server's backup folder, then restored into a throwaway database and compared.</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if b}
		<BackupAlert status={b} {tz} />
		<dl data-testid="backup-status">
			<dt>State</dt>
			<dd>{summary[b.state]}</dd>
			<dt>Last good backup</dt>
			<dd>
				{#if b.last_good_at}<time datetime={isoAttr(b.last_good_at)}>{dateTime(b.last_good_at, tz)}</time>{:else}None yet{/if}
			</dd>
			{#if b.last_run_at}
				<dt>Last run</dt>
				<dd><time datetime={isoAttr(b.last_run_at)}>{dateTime(b.last_run_at, tz)}</time></dd>
			{/if}
			{#if b.last_good_at}
				<dt>Its size</dt>
				<dd>{(b.last_good_bytes / 1048576).toLocaleString('en-AU', { maximumFractionDigits: 1 })} MiB</dd>
				<dt>Nights kept</dt>
				<dd>{b.nights}</dd>
			{/if}
		</dl>

		{#if b.state !== 'off'}
			<section aria-labelledby="now-heading">
				<h2 id="now-heading">Back up now</h2>
				{#if b.run === 'off'}
					<p class="note" data-testid="backup-now">This server's compose.yaml is from before Back up now. Upgrade with the latest release's compose.yaml to use it.</p>
				{:else}
					<p class="note">
						The same backup and check as the nightly one, kept with the nightly backups. It replaces any backup already made today. Use it before a change you might want to
						undo, such as an upgrade.
					</p>
					<button type="button" onclick={backUpNow} disabled={busy || b.run !== 'idle'} aria-describedby="now-state">Back up now</button>
					<p id="now-state" role="status" data-testid="backup-now">
						{#if b.run === 'queued'}
							{#if stuck}
								Asked for at <time datetime={isoAttr(b.requested_at!)}>{dateTime(b.requested_at!, tz)}</time>, but the backup service hasn't started it. Is it running?
								<code>docker compose ps backup</code>
							{:else}
								Asked for at <time datetime={isoAttr(b.requested_at!)}>{dateTime(b.requested_at!, tz)}</time>. The backup service starts it within a minute.
							{/if}
						{:else if b.run === 'running'}
							Running since <time datetime={isoAttr(b.running_since!)}>{dateTime(b.running_since!, tz)}</time>: the dumps, then the check.
							This page shows the result when it's done.
						{:else if watching && b.state === 'failed'}
							<span class="warn">Done, but it failed: {b.error}.</span>
						{:else if watching && b.last_run_at}
							Done: the backup at <time datetime={isoAttr(b.last_run_at)}>{dateTime(b.last_run_at, tz)}</time> was good.
						{/if}
					</p>
				{/if}
			</section>
		{/if}
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
	.note {
		color: var(--muted);
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--space-xs) var(--space-md);
	}
	dt {
		font-weight: var(--weight-strong);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	button {
		min-height: var(--target);
	}
	.warn {
		color: var(--warn-fg);
		background: var(--warn-bg);
	}
	code {
		overflow-wrap: anywhere;
	}
</style>
