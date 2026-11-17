<script lang="ts">
	// R23c: the re-parse of stored messages after an alias, template or
	// profile changes. Its state, how far it has got, its rate and time left,
	// and an admin's Pause, Resume, Stop and Start again. Counts and reasons
	// only: never a page or a capcode (P1-P3). On phones too.
	import { api, type Meta, type ReparseStatus } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	// How often the page asks while a run is going, and while it's held.
	const POLL_MS = 2000;
	const HELD_MS = 10000;

	let meta: Meta | undefined = $state();
	let r: ReparseStatus | undefined = $state();
	let error = $state('');
	let busy = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');
	api.meta().then((m) => (meta = m));
	function refresh() {
		api
			.reparse()
			.then((x) => {
				r = x;
				error = '';
			})
			.catch((e) => (error = e.message));
	}
	refresh();
	$effect(() => {
		if (!r) return;
		const t = setTimeout(refresh, r.state === 'running' ? POLL_MS : HELD_MS);
		return () => clearTimeout(t);
	});

	const asks: Record<'stop' | 'start', string> = {
		stop: 'Stop the re-parse? The pages it has done stay done; the rest stay as they were parsed before until it is started again from the beginning.',
		start: 'Start the re-parse again from the beginning? Every page it covers is re-parsed again, with any changes waiting for it.'
	};
	async function act(action: 'pause' | 'resume' | 'stop' | 'start') {
		if ((action === 'stop' || action === 'start') && !confirm(asks[action])) return;
		busy = true;
		error = '';
		try {
			r = await api.reparseAction(action);
		} catch (e) {
			error = (e as Error).message;
			refresh();
		} finally {
			busy = false;
		}
	}

	const states: Record<ReparseStatus['state'], string> = {
		idle: 'Idle',
		running: 'Running',
		paused: 'Paused',
		stopped: 'Stopped'
	};
	const says: Record<ReparseStatus['state'], string> = {
		idle: 'Nothing to re-parse now.',
		running: 'Re-parsing in the background. A restart or an upgrade carries on from where it got to.',
		paused: 'Paused: it carries on from where it got to when resumed, even after a restart.',
		stopped: 'Stopped: the rest of its pages stay as they were parsed before. Start again runs it from the beginning.'
	};
	// R23b's reason codes, in words.
	const reasonWords: Record<string, string> = {
		profiles_changed: 'the profiles changed',
		retry: 'a retry',
		alias_added: 'an alias added',
		alias_capcode_changed: "an alias's capcode changed",
		alias_template_changed: "an alias's template changed",
		alias_ignored_changed: 'an alias marked ignored or not',
		alias_deleted: 'an alias deleted',
		alias_import: 'an alias import',
		template_profile_changed: "a template's agency profile changed",
		other: 'another tool'
	};
	const n = (x: number) => x.toLocaleString('en-AU');
	function duration(s: number): string {
		if (s < 60) return 'under a minute';
		const h = Math.floor(s / 3600);
		const m = Math.round((s % 3600) / 60);
		return h > 0 ? `about ${h} h ${m} min` : `about ${m} min`;
	}
	const hasRun = $derived(!!r?.started_at);
</script>

<svelte:head><title>Re-parse · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Re-parse</h1>
	<p class="note">
		Admins only. When an alias, template or profile changes, the server re-parses the stored messages it affects, in the background. A profile change re-parses the whole
		archive, which takes hours on a large one. The same is in Grafana, on Requests and load.
	</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if r}
		<dl data-testid="reparse-status">
			<dt>State</dt>
			<dd><strong class="state {r.state}" data-testid="reparse-state">{states[r.state]}</strong> <span class="says">{says[r.state]}</span></dd>
			{#if hasRun}
				<dt>{r.state === 'idle' ? 'Last run' : 'Done'}</dt>
				<dd data-testid="reparse-done">
					{n(r.done)} of {n(r.total)} messages ({r.percent} %)
					<progress max="100" value={r.percent} aria-label="Re-parse progress">{r.percent} %</progress>
				</dd>
				{#if r.state === 'running'}
					<dt>Rate</dt>
					<dd data-testid="reparse-rate">{r.per_minute > 0 ? `${n(r.per_minute)} a minute` : 'Working it out'}</dd>
					<dt>Time left</dt>
					<dd data-testid="reparse-eta">{r.eta_seconds === null ? 'Working it out' : duration(r.eta_seconds)}</dd>
				{/if}
				<dt>Started</dt>
				<dd><time datetime={isoAttr(r.started_at!)}>{dateTime(r.started_at!, tz)}</time></dd>
				{#if r.finished_at && r.state === 'idle'}
					<dt>Finished</dt>
					<dd><time datetime={isoAttr(r.finished_at)}>{dateTime(r.finished_at, tz)}</time></dd>
				{:else if r.changed_at && r.state !== 'running'}
					<dt>Since</dt>
					<dd><time datetime={isoAttr(r.changed_at)}>{dateTime(r.changed_at, tz)}</time></dd>
				{/if}
				<dt>Why</dt>
				<dd>{r.reasons.map((c) => reasonWords[c] ?? c).join(', ') || 'Not known'}</dd>
			{/if}
			{#if r.waiting > 0}
				<dt>Waiting</dt>
				<dd data-testid="reparse-waiting">
					{r.waiting} {r.waiting === 1 ? 'change waits' : 'changes wait'} for the next re-parse{r.state === 'idle' ? ', which starts in a moment' : ''}.
				</dd>
			{/if}
		</dl>

		{#if hasRun}
			<div class="actions" role="group" aria-label="Re-parse controls">
				{#if r.state === 'running'}
					<button type="button" class="secondary" onclick={() => act('pause')} disabled={busy}>Pause</button>
				{/if}
				{#if r.state === 'paused'}
					<button type="button" onclick={() => act('resume')} disabled={busy}>Resume</button>
				{/if}
				{#if r.state === 'running' || r.state === 'paused'}
					<button type="button" class="danger" onclick={() => act('stop')} disabled={busy}>Stop</button>
				{/if}
				<button type="button" class="secondary" onclick={() => act('start')} disabled={busy}>Start again</button>
			</div>
			<p class="note">
				Pause holds it where it is, and Resume carries on from there. Stop cancels it: each page is done or not, never half done. Start again runs it from the beginning.
			</p>
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
	.says {
		color: var(--muted);
	}
	.state.paused,
	.state.stopped {
		padding: 0 var(--space-xs);
		border-radius: var(--radius-small);
		background: var(--warn-bg);
		color: var(--warn-fg);
	}
	progress {
		display: block;
		width: 100%;
		max-width: var(--field-wide);
		margin-top: var(--space-2xs);
		accent-color: var(--accent);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
	}
</style>
