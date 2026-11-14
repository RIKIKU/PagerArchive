<script lang="ts">
	// R84h: automatic upgrades, which run on the host (docs/deploy.md,
	// "Automatic upgrades"), never in the API: whether they're on, the last
	// check and upgrade, and any that failed and were rolled back. Switching
	// them on or off, and trying a failed release again, is done on the server.
	import { api, type AutoUpgradeStatus, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import UpgradeAlert from '#lib/UpgradeAlert.svelte';

	let meta: Meta | undefined = $state();
	let u: AutoUpgradeStatus | undefined = $state();
	let error = $state('');
	const tz = $derived(meta?.time_zone ?? 'UTC');
	api.meta().then((m) => (meta = m));
	api
		.autoUpgrade()
		.then((x) => (u = x))
		.catch((e) => (error = e.message));

	const summary: Record<AutoUpgradeStatus['state'], string> = {
		off: 'Automatic upgrades are off.',
		ok: 'On: checking for a new release every hour.',
		failed: 'On, but the last upgrade failed.',
		stale: "On, but they haven't checked for more than 3 hours.",
		stopped: 'On, but an upgrade stopped part way.'
	};
	const doing: Record<AutoUpgradeStatus['run'], string> = {
		idle: '',
		checking: 'Checking for a new release',
		verifying: 'Checking the new release’s attestations',
		'backing-up': 'Backing up before the upgrade',
		upgrading: 'Upgrading',
		'health-check': 'Checking the upgraded install',
		'rolling-back': 'Rolling back'
	};
	const results: Record<string, string> = { upgraded: 'Upgraded', 'rolled-back': 'Failed, rolled back', 'rollback-failed': 'Failed, and the rollback failed' };
</script>

<svelte:head><title>Upgrades · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Upgrades</h1>
	<p class="note">
		Admins only. When they're switched on, the server checks every hour for a new release, checks its attestations, backs up, upgrades, and checks the result. If that
		fails, it goes back to the version before.
	</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if u}
		<UpgradeAlert status={u} {tz} />
		<dl data-testid="upgrade-status">
			<dt>State</dt>
			<dd>{summary[u.state]}</dd>
			{#if u.state === 'off' && u.disabled_at}
				<dt>Switched off</dt>
				<dd><time datetime={isoAttr(u.disabled_at)}>{dateTime(u.disabled_at, tz)}</time></dd>
			{/if}
			{#if u.run !== 'idle' && u.state !== 'off'}
				<dt>Now</dt>
				<dd>
					{doing[u.run]}{#if u.running_since}, since <time datetime={isoAttr(u.running_since)}>{dateTime(u.running_since, tz)}</time>{/if}
				</dd>
			{/if}
			{#if u.last_check_at}
				<dt>Last check</dt>
				<dd>
					<time datetime={isoAttr(u.last_check_at)}>{dateTime(u.last_check_at, tz)}</time>{#if u.last_check_note}: {u.last_check_note}{/if}
				</dd>
			{/if}
			{#if u.running_version}
				<dt>Running</dt>
				<dd>{u.running_version}</dd>
			{/if}
			<dt>Last upgrade</dt>
			<dd>
				{#if u.last_upgrade}
					<time datetime={isoAttr(u.last_upgrade.at)}>{dateTime(u.last_upgrade.at, tz)}</time>: {u.last_upgrade.from} to {u.last_upgrade.to}
				{:else}
					None yet
				{/if}
			</dd>
			{#if u.failures > 0}
				<dt>Failed</dt>
				<dd>{u.failures} {u.failures === 1 ? 'upgrade' : 'upgrades'}, {u.rollbacks} rolled back</dd>
			{/if}
			{#if u.failed_versions.length}
				<dt>Not tried again</dt>
				<dd>{u.failed_versions.join(', ')}</dd>
			{/if}
		</dl>

		{#if u.history.length}
			<section aria-labelledby="history-heading">
				<h2 id="history-heading">History</h2>
				<table data-testid="upgrade-history">
					<thead><tr><th scope="col">When</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Result</th></tr></thead>
					<tbody>
						{#each [...u.history].reverse() as h (h.at + h.to)}
							<tr>
								<td><time datetime={isoAttr(h.at)}>{dateTime(h.at, tz)}</time></td>
								<td>{h.from}</td>
								<td>{h.to}</td>
								<td class:warn={h.result !== 'upgraded'}>{results[h.result] ?? h.result}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</section>
		{/if}

		<section aria-labelledby="server-heading">
			<h2 id="server-heading">On the server</h2>
			<p class="note">
				{#if u.state === 'off'}
					To switch them on, from the folder with compose.yaml: <code>deploy/auto-upgrade/auto-upgrade.sh enable</code>.
				{:else}
					To switch them off: <code>deploy/auto-upgrade/auto-upgrade.sh disable</code>. To try a release that failed again:
					<code>deploy/auto-upgrade/auto-upgrade.sh retry 1.2.3</code>.
				{/if}
				A manual upgrade works as it always has. See docs/deploy.md, "Automatic upgrades".
			</p>
		</section>
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
	table {
		border-collapse: collapse;
	}
	th,
	td {
		padding: var(--space-2xs) var(--space-md) var(--space-2xs) 0;
		text-align: left;
	}
	.warn {
		color: var(--warn-fg);
		background: var(--warn-bg);
	}
	code {
		overflow-wrap: anywhere;
	}
	.error {
		color: var(--warn-fg);
	}
</style>
