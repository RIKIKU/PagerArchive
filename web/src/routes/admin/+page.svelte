<script lang="ts">
	// R44b: the Administration start page links to every admin screen.
	import { api, type AutoUpgradeStatus, type BackupStatus, type ClientStatus, type Meta } from '#lib/api.ts';
	import BackupAlert from '#lib/BackupAlert.svelte';
	import GroupPage from '#lib/GroupPage.svelte';
	import { groups } from '#lib/nav.ts';
	import UpgradeAlert from '#lib/UpgradeAlert.svelte';
	import { refreshWaiting, waiting } from '#lib/waiting.svelte.ts';

	let quiet = $state(0);
	api
		.clients()
		.then((cs: ClientStatus[]) => (quiet = cs.filter((c) => !c.receiving).length))
		.catch(() => {});
	refreshWaiting();
	let meta: Meta | undefined = $state();
	let backup: BackupStatus | undefined = $state();
	// R84h: automatic upgrades, which run on the host.
	let upgrade: AutoUpgradeStatus | undefined = $state();
	api.meta().then((m) => (meta = m));
	api
		.backup()
		.then((b) => (backup = b))
		.catch(() => {});
	api
		.autoUpgrade()
		.then((u) => (upgrade = u))
		.catch(() => {});
	const upgradeTag: Record<AutoUpgradeStatus['state'], string> = { off: 'off', ok: '', failed: 'failed', stale: 'not checking', stopped: 'needs you' };
	// R64: templates whose names differ from another's only in letter case.
	let sameName = $state(0);
	api
		.adminTemplates()
		.then((ts) => (sameName = ts.filter((t) => t.same_name_as?.length).length))
		.catch(() => {});
</script>

{#if backup}<BackupAlert status={backup} tz={meta?.time_zone} />{/if}
{#if upgrade}<UpgradeAlert status={upgrade} tz={meta?.time_zone} />{/if}

<GroupPage group={groups.find((g) => g.id === 'admin')!} testid="admin-screens">
	{#snippet extra(e)}
		{#if e.href === '/admin/templates' && sameName > 0}<span class="tag" data-testid="case-tag">{sameName} named alike, ignoring case</span>{/if}
		{#if e.href === '/admin/clients' && quiet > 0}<span class="tag">{quiet} not receiving</span>{/if}
		{#if e.href === '/admin/backups' && (backup?.state === 'failed' || backup?.state === 'overdue')}<span class="tag">{backup.state}</span>{/if}
		{#if e.href === '/admin/upgrades' && upgrade && upgrade.state !== 'ok'}<span class="tag" class:off={upgrade.state === 'off'} data-testid="upgrade-tag">{upgradeTag[upgrade.state]}</span>{/if}
		{#if e.href === '/admin/requests' && waiting.count > 0}<span class="tag" data-testid="waiting-tag">{waiting.count} waiting</span>{/if}
	{/snippet}
</GroupPage>

<style>
	.tag {
		margin-left: var(--space-sm);
		padding: 0 var(--space-sm);
		border-radius: var(--radius-pill);
		background: var(--warn-bg);
		color: var(--warn-fg);
		font-size: var(--text-xs);
		font-weight: var(--weight-strong);
	}
	/* "off" is a fact, not a warning. */
	.tag.off {
		background: var(--surface-2);
		color: var(--muted);
		font-weight: var(--weight-normal);
	}
</style>
