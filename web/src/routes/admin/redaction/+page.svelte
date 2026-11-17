<script lang="ts">
	// R45a: retroactive redaction, the nightly run's check of every stored
	// message after the redaction rules change (P1). Its state, how far it
	// has got, its rate and time left, and an admin's Pause, Resume, Stop
	// and Start again. Counts only: never a page, a rule or a capcode
	// (P1-P3). On phones too.
	import { api, type Meta, type RedactionStatus } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	// How often the page asks while a run is going, and while it's held.
	const POLL_MS = 2000;
	const HELD_MS = 10000;

	let meta: Meta | undefined = $state();
	let r: RedactionStatus | undefined = $state();
	let error = $state('');
	let busy = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');
	api.meta().then((m) => (meta = m));
	function refresh() {
		api
			.redaction()
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
		stop: 'Stop retroactive redaction? The messages it has done stay done; the rest keep what the rules remove until the next nightly run starts it again from the beginning.',
		start: 'Start retroactive redaction again from the beginning? Every stored message is checked again against the rules in use.'
	};
	async function act(action: 'pause' | 'resume' | 'stop' | 'start') {
		if ((action === 'stop' || action === 'start') && !confirm(asks[action])) return;
		busy = true;
		error = '';
		try {
			r = await api.redactionAction(action);
		} catch (e) {
			error = (e as Error).message;
			refresh();
		} finally {
			busy = false;
		}
	}

	const states: Record<RedactionStatus['state'], string> = {
		idle: 'Idle',
		running: 'Running',
		paused: 'Paused',
		stopped: 'Stopped'
	};
	const says: Record<RedactionStatus['state'], string> = {
		idle: 'Nothing to do now.',
		running: 'Checking the stored messages in the background. A restart or an upgrade carries on from where it got to.',
		paused: 'Paused: it carries on from where it got to when resumed, even after a restart. The nightly run leaves it paused.',
		stopped: 'Stopped: the messages it did stay done. The next nightly run starts it again from the beginning, or Start again does now.'
	};
	const reasonWords: Record<string, string> = {
		rules_changed: 'the redaction rules changed',
		started_again: 'an admin started it again'
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

<svelte:head><title>Retroactive redaction · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Retroactive redaction</h1>
	<p class="note">
		Admins only. When the network profile's redaction rules change, the nightly run checks every stored message against them and removes what they match, from the text and
		everything parsed from it, in the background. On a large archive that takes hours. The same is in Grafana, on Requests and load.
	</p>
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if r}
		<dl data-testid="redaction-status">
			<dt>State</dt>
			<dd><strong class="state {r.state}" data-testid="redaction-state">{states[r.state]}</strong> <span class="says">{says[r.state]}</span></dd>
			{#if hasRun}
				<dt>{r.state === 'idle' ? 'Last run' : 'Done'}</dt>
				<dd data-testid="redaction-done">
					{n(r.done)} of {n(r.total)} messages ({r.percent} %)
					<progress max="100" value={r.percent} aria-label="Retroactive redaction progress">{r.percent} %</progress>
				</dd>
				<dt>Changed</dt>
				<dd data-testid="redaction-changed">{n(r.redacted)} {r.redacted === 1 ? 'message' : 'messages'}</dd>
				{#if r.state === 'running'}
					<dt>Rate</dt>
					<dd data-testid="redaction-rate">{r.per_minute > 0 ? `${n(r.per_minute)} a minute` : 'Working it out'}</dd>
					<dt>Time left</dt>
					<dd data-testid="redaction-eta">{r.eta_seconds === null ? 'Working it out' : duration(r.eta_seconds)}</dd>
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
				<dd>{reasonWords[r.reason] ?? 'Not known'}</dd>
			{/if}
			{#if r.pending && r.state === 'idle'}
				<dt>Waiting</dt>
				<dd data-testid="redaction-waiting">The rules in use aren't applied to every stored message yet: the next nightly run starts it.</dd>
			{/if}
		</dl>

		<div class="actions" role="group" aria-label="Retroactive redaction controls">
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
			Pause holds it where it is, and Resume carries on from there. Stop cancels it: each message is done or not, its text and what's parsed from it together. Start again checks
			every message again from the beginning.
		</p>
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
