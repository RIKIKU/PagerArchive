<script lang="ts">
	// R84h: what an admin is told when an automatic upgrade failed, was rolled
	// back, or stopped part way, or the hourly checks have stopped.
	import type { AutoUpgradeStatus } from '#lib/api.ts';
	import { dateTime } from '#lib/format.ts';

	let { status, tz = 'UTC' }: { status: AutoUpgradeStatus; tz?: string } = $props();
	const f = $derived(status.last_failure);
</script>

{#if status.state === 'failed' && f}
	<p class="alert" role="alert" data-testid="upgrade-alert">
		{#if !f.to}
			Automatic upgrades: {f.reason}.
		{:else if f.rolled_back}
			The automatic upgrade to {f.to} failed{f.at ? ` at ${dateTime(f.at, tz)}` : ''}: {f.reason}. It was rolled back to {f.from}{f.restored ? ', and the database restored from the backup made just before' : ''},
			and {f.to} won't be tried again.
		{:else}
			The automatic upgrade to {f.to} failed: {f.reason}. Rolling back failed too: {f.rollback_error}. It needs you on the server.
		{/if}
		<a href="/admin/upgrades">Upgrades</a>
	</p>
{:else if status.state === 'stopped'}
	<p class="alert" role="alert" data-testid="upgrade-alert">An automatic upgrade stopped part way, and needs you on the server. <a href="/admin/upgrades">Upgrades</a></p>
{:else if status.state === 'stale'}
	<p class="alert" role="alert" data-testid="upgrade-alert">
		Automatic upgrades haven't checked for a release for more than 3 hours{status.last_check_at ? `: the last check was ${dateTime(status.last_check_at, tz)}` : ''}. Is their timer running?
		<a href="/admin/upgrades">Upgrades</a>
	</p>
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
