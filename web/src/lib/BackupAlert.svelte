<script lang="ts">
	// R86a: what an admin is told when the backups aren't working.
	import type { BackupStatus } from '#lib/api.ts';
	import { dateTime } from '#lib/format.ts';

	let { status, tz = 'UTC' }: { status: BackupStatus; tz?: string } = $props();
	const last = $derived(status.last_good_at ? `The last good backup was ${dateTime(status.last_good_at, tz)}.` : 'There is no good backup yet.');
</script>

{#if status.state === 'failed'}
	<p class="alert" role="alert" data-testid="backup-alert">The last backup failed: {status.error}. {last} <a href="/admin/backups">Backups</a></p>
{:else if status.state === 'overdue'}
	<p class="alert" role="alert" data-testid="backup-alert">Backups are overdue: none has succeeded for more than 36 hours. {last} <a href="/admin/backups">Backups</a></p>
{/if}

<style>
	.alert {
		margin: var(--space-md) 0;
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius);
		background: var(--warn-bg);
		color: var(--warn-fg);
		/* Must-notice: an edge at least 4.5:1 against the page (ArchiveUx, owner 2026-11-20). */
		border: var(--border-width-strong) solid var(--alert-edge);
	}
	.alert a {
		color: inherit;
	}
</style>
