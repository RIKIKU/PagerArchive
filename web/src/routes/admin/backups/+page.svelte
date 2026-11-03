<script lang="ts">
	// R86a: whether the nightly backups are working. Restoring one is done on
	// the server (docs/deploy.md, "Backups").
	import { api, type BackupStatus, type Meta } from '#lib/api.ts';
	import BackupAlert from '#lib/BackupAlert.svelte';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let meta: Meta | undefined = $state();
	let b: BackupStatus | undefined = $state();
	let error = $state('');
	const tz = $derived(meta?.time_zone ?? 'UTC');
	api.meta().then((m) => (meta = m));
	api
		.backup()
		.then((x) => (b = x))
		.catch((e) => (error = e.message));
	const summary: Record<BackupStatus['state'], string> = {
		ok: 'Working.',
		waiting: 'No backup yet: the first is tonight.',
		failed: 'The last backup failed.',
		overdue: 'Overdue.',
		off: 'This server has no backup service.'
	};
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
	}
</style>
