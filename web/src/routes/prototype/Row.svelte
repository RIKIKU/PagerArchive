<script lang="ts">
	import type { Message } from '#lib/api.ts';
	import { date, time, isoAttr } from '#lib/format.ts';
	import Badge from './Badge.svelte';

	// R31 and R35d, as the real list has them: the whole row opens the
	// message, the alias and the job number are links of their own.
	let { m, tz, selected, open, alias, job }: { m: Message; tz: string; selected: boolean; open: string; alias: string; job: string } =
		$props();

	const prio = $derived(
		m.priority === 'Emergency' ? 'emergency' : m.priority === 'Non-emergency' ? 'nonemergency' : m.priority === 'Admin' ? 'admin' : 'plain'
	);
</script>

<article class="p-row {prio}" class:selected aria-labelledby="pm{m.id}-who">
	<div class="line">
		<Badge priority={m.priority} heartbeat={m.heartbeat} />
		<time datetime={isoAttr(m.sent_at)}><span class="date">{date(m.sent_at, tz)}</span> {time(m.sent_at, tz)}</time>
		<span class="who-wrap">
			{#if m.icon}<span class="p-tile"><img src={m.icon} alt={m.agency ?? ''} width="32" height="32" /></span>{/if}
			<a id="pm{m.id}-who" class="who" href={alias} style:--agency={m.colour ?? 'transparent'}>{m.alias ?? m.capcode}</a>
		</span>
		{#if m.group}<span class="group">{m.group}</span>{/if}
		{#if m.job_number}<a class="job" href={job}>{m.job_number}</a>{/if}
	</div>
	<p class="text">
		<a class="open" href={open} aria-current={selected ? 'true' : undefined}>{m.display_text || '(no text)'}</a>
	</p>
</article>
