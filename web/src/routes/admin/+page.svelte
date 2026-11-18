<script lang="ts">
	// R44b: the Administration start page links to every admin screen.
	import { api, type AdminAreaLayer, type AutoUpgradeStatus, type BackupStatus, type ClientStatus, type Meta, type RedactionStatus, type ReparseStatus } from '#lib/api.ts';
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
	// R23c: the re-parse, while one runs or is held.
	let reparse: ReparseStatus | undefined = $state();
	api
		.reparse()
		.then((r) => (reparse = r))
		.catch(() => {});
	// R45a: retroactive redaction, while one runs or is held.
	let redaction: RedactionStatus | undefined = $state();
	api
		.redaction()
		.then((r) => (redaction = r))
		.catch(() => {});
	// R64: templates whose names differ from another's only in letter case.
	let sameName = $state(0);
	api
		.adminTemplates()
		.then((ts) => (sameName = ts.filter((t) => t.same_name_as?.length).length))
		.catch(() => {});
	// R27b: aliases sharing a name, ignoring letter case.
	let sharedNames = $state(0);
	api
		.sharedNames()
		.then((s) => (sharedNames = s.reduce((n, g) => n + g.aliases.length, 0)))
		.catch(() => {});
	// R88e: the network profile's layers of areas, if it names some.
	let areas: AdminAreaLayer[] = $state([]);
	api
		.adminAreas()
		.then((a) => (areas = a.layers))
		.catch(() => {});
</script>

{#if backup}<BackupAlert status={backup} tz={meta?.time_zone} />{/if}
{#if upgrade}<UpgradeAlert status={upgrade} tz={meta?.time_zone} />{/if}

<GroupPage group={groups.find((g) => g.id === 'admin')!} testid="admin-screens">
	{#snippet extra(e)}
		{#if e.href === '/admin/aliases' && sharedNames > 0}<span class="tag" data-testid="shared-names-tag">{sharedNames} share names</span>{/if}
		{#if e.href === '/admin/templates' && sameName > 0}<span class="tag" data-testid="case-tag">{sameName} named alike, ignoring case</span>{/if}
		{#if e.href === '/admin/clients' && quiet > 0}<span class="tag">{quiet} not receiving</span>{/if}
		{#if e.href === '/admin/backups' && (backup?.state === 'failed' || backup?.state === 'overdue')}<span class="tag">{backup.state}</span>{/if}
		{#if e.href === '/admin/upgrades' && upgrade && upgrade.state !== 'ok'}<span class="tag" class:off={upgrade.state === 'off'} data-testid="upgrade-tag">{upgradeTag[upgrade.state]}</span>{/if}
		{#if e.href === '/admin/reparse' && reparse && reparse.state !== 'idle'}<span class="tag" class:off={reparse.state === 'running'} data-testid="reparse-tag"
				>{reparse.state === 'running' ? `running, ${reparse.percent} %` : reparse.state}</span
			>{/if}
		{#if e.href === '/admin/redaction' && redaction && redaction.state !== 'idle'}<span class="tag" class:off={redaction.state === 'running'} data-testid="redaction-tag"
				>{redaction.state === 'running' ? `running, ${redaction.percent} %` : redaction.state}</span
			>{/if}
		{#if e.href === '/admin/requests' && waiting.count > 0}<span class="tag" data-testid="waiting-tag">{waiting.count} waiting</span>{/if}
	{/snippet}
</GroupPage>

{#if areas.length}
	<section class="areas" aria-labelledby="areas-h" data-testid="admin-areas">
		<h2 id="areas-h">Areas on the map</h2>
		{#each areas as a (a.id)}
			<div class="layer" data-testid="admin-areas-{a.id}">
				<h3>{a.label} <span class="outline">({a.outline} outline)</span></h3>
				{#if a.error}
					<p class="failed" role="status">
						<strong>{a.label} areas didn't load</strong> from <code>{a.source}</code>{a.stale
							? '; the ones loaded before are shown until the file is put right'
							: ''}: {a.error}
					</p>
				{/if}
				{#if a.loaded}
					<p>
						{a.names.length} {a.label} outline{a.names.length === 1 ? '' : 's'}{a.stale ? '' : ' from the network profile\'s'}
						{#if !a.stale}<code>{a.source}</code>{/if}{a.pending ? "; stored pages' areas are still being worked out" : ''}.
					</p>
					<p class="names">{a.names.join(', ')}</p>
				{/if}
				<p class="credit">{a.attribution}</p>
			</div>
		{/each}
	</section>
{/if}

<style>
	.areas {
		max-width: var(--measure);
		margin-top: var(--space-xl);
	}
	.areas h2 {
		font-size: var(--text-lg);
	}
	.areas h3 {
		margin: var(--space-md) 0 0;
		font-size: var(--text-md);
	}
	.areas p {
		margin: var(--space-xs) 0;
	}
	.outline {
		color: var(--muted);
		font-size: var(--text-sm);
		font-weight: var(--weight-normal);
	}
	.failed {
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius);
		background: var(--warn-bg);
		color: var(--warn-fg);
		overflow-wrap: anywhere;
	}
	.names,
	.credit {
		color: var(--muted);
		font-size: var(--text-sm);
	}
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
