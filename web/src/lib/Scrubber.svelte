<script lang="ts">
	// R81b-R81d: the scrubber, a strip down the right of a message list for
	// large jumps. Its marks cover the last 30 days, or the search's date range,
	// newest at the top: days, weeks or months, with today and yesterday by the
	// hour. Each mark's dots say how busy it is (R81c), counted by the API for
	// the list's own search and kept up to date by its live updates. Clicking or
	// dragging jumps the list to a mark; while dragging, the mark under the
	// pointer is named. It's one vertical slider for the keyboard: the arrow
	// keys move a mark, Page Up and Page Down a week, Home and End to the ends.
	// At an hour of today or yesterday, a button zooms into its twelve 5-minute
	// blocks (R81d), with the hours either side a click away.
	import { untrack } from 'svelte';
	import { api, type Counts, type Mark } from './api.ts';
	import Icon from './Icon.svelte';

	let {
		query,
		tz,
		at,
		onjump
	}: {
		/** The list's API parameters, as a string (MessageList's askedKey). */
		query: string;
		tz: string;
		/** When the list's first row in view was sent; none at the top. */
		at?: string;
		/** Jump the list to the messages sent before this time. */
		onjump: (before: string) => void;
	} = $props();

	let counts: Counts | undefined = $state();
	let zoom: { hour: string; counts: Counts } | undefined = $state();
	let dragging = $state(false);
	let hover: number | undefined = $state();
	// The mark the keyboard or a jump moved to, until the list gets there.
	let picked: number | undefined = $state();
	let track: HTMLElement | undefined = $state();
	let error = $state('');
	// Escape hides the tooltips until the pointer or focus moves on (WCAG 1.4.13).
	let tipOff = $state(false);

	const marks = $derived(zoom ? zoom.counts.marks : (counts?.marks ?? []));
	const counted = $derived(zoom ? zoom.counts.counted : (counts?.counted ?? true));
	const ms = (iso: string) => Date.parse(iso);

	// Counts that took too long (from a cold disk, say) are asked for once
	// more a moment later, when what the first try read is in the caches.
	let retry: ReturnType<typeof setTimeout> | undefined;
	function load(again = true) {
		const q = new URLSearchParams(query);
		clearTimeout(retry);
		api
			.counts(q)
			.then((c) => {
				if (query !== q.toString()) return;
				counts = c;
				error = '';
				if (!c.counted && again) retry = setTimeout(() => load(false), 3000);
			})
			.catch(() => (error = 'The marks couldn’t be loaded.'));
	}
	// A new search: its marks, and out of any zoom.
	$effect(() => {
		void query;
		untrack(() => {
			zoom = undefined;
			picked = undefined;
			load();
		});
		return () => clearTimeout(retry);
	});

	function loadZoom(hour: string) {
		api
			.counts(new URLSearchParams(query), hour)
			.then((c) => {
				zoom = { hour, counts: c };
				picked = undefined;
			})
			.catch(() => (error = 'That hour couldn’t be loaded.'));
	}

	/** R81c: a live message adds to its mark; one newer than every mark (a new hour or day) loads them again. */
	export function arrived(sentAt: string) {
		const t = ms(sentAt);
		let found = false;
		for (const c of [counts, zoom?.counts]) {
			if (!c) continue;
			const m = c.marks.find((x) => ms(x.start) <= t && t < ms(x.end));
			if (m) {
				m.count++;
				found = true;
			}
		}
		if (!found && counts && counts.marks.length > 0 && t >= ms(counts.marks[0].end)) load();
	}

	/** The list reconnected: what it missed is counted again. */
	export function reload() {
		load();
		if (zoom) loadZoom(zoom.hour);
	}

	// Where the list is: the mark holding its first row in view, or the newest.
	const current = $derived.by(() => {
		if (marks.length === 0) return undefined;
		if (!at) return 0;
		const t = ms(at);
		const i = marks.findIndex((m) => ms(m.start) <= t && t < ms(m.end));
		if (i >= 0) return i;
		return t >= ms(marks[0].end) ? 0 : t < ms(marks[marks.length - 1].start) ? marks.length - 1 : undefined;
	});
	const shown = $derived(dragging && hover !== undefined ? hover : (picked ?? current));

	// How busy: up to four dots, against the busiest mark of the same kind
	// (an hour against hours, a day against days).
	const busiest = $derived.by(() => {
		const out: Record<string, number> = {};
		for (const m of marks) out[m.kind] = Math.max(out[m.kind] ?? 0, m.count);
		return out;
	});
	const dots = (m: Mark) => (!counted || m.count === 0 ? 0 : Math.min(4, 1 + Math.floor((3 * m.count) / Math.max(1, busiest[m.kind]))));

	// Names, in the deployment's time zone (R35a).
	const fmt = (opts: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('en-AU', { timeZone: tz, ...opts });
	const dayKey = (iso: string) => fmt({ year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(iso));
	const todayKey = $derived(dayKey(new Date().toISOString()));
	const yesterdayKey = $derived(dayKey(new Date(Date.now() - 864e5).toISOString()));
	function whichDay(iso: string) {
		const k = dayKey(iso);
		return k === todayKey ? 'Today' : k === yesterdayKey ? 'Yesterday' : fmt({ weekday: 'short', day: 'numeric', month: 'short' }).format(new Date(iso));
	}
	const hhmm = (iso: string) => fmt({ hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(iso));
	function name(m: Mark): string {
		const d = new Date(m.start);
		switch (m.kind) {
			case 'hour':
				return `${whichDay(m.start)} ${hhmm(m.start)}`;
			case 'block':
				return `${whichDay(m.start)} ${hhmm(m.start)} to ${hhmm(m.end)}`;
			case 'day':
				return fmt({ weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }).format(d);
			case 'week':
				return `Week from ${fmt({ day: 'numeric', month: 'long', year: 'numeric' }).format(d)}`;
			case 'month':
				return fmt({ month: 'long', year: 'numeric' }).format(d);
		}
	}
	const plural = (n: number) => `${n} message${n === 1 ? '' : 's'}`;
	const spoken = (m: Mark) => (counted ? `${name(m)}, ${plural(m.count)}` : name(m));

	// A few labels on the strip, so it can be read at a glance.
	function label(m: Mark, i: number): string {
		const prev = marks[i - 1];
		switch (m.kind) {
			case 'block':
				return i === 0 || i === marks.length - 1 || hhmm(m.start).endsWith(':30') ? hhmm(m.start) : '';
			case 'hour':
				return !prev || prev.kind !== 'hour' || dayKey(prev.start) !== dayKey(m.start) ? whichDay(m.start) : '';
			case 'day': {
				const d = new Date(m.start);
				return !prev || prev.kind !== 'day' || fmt({ weekday: 'short' }).format(d) === 'Mon' ? fmt({ day: 'numeric', month: 'short' }).format(d) : '';
			}
			case 'week': {
				const month = fmt({ month: 'short' }).format(new Date(m.start));
				return !prev || prev.kind !== 'week' || fmt({ month: 'short' }).format(new Date(prev.start)) !== month ? month : '';
			}
			case 'month': {
				const d = new Date(m.start);
				const month = fmt({ month: 'numeric' }).format(d);
				return month === '1' || !prev || prev.kind !== 'month' ? fmt({ year: 'numeric' }).format(d) : '';
			}
		}
	}

	// R81d: the list is at an hour of today or yesterday.
	const zoomable = $derived(!zoom && current !== undefined && marks[current]?.kind === 'hour' ? marks[current] : undefined);
	const hourMarks = $derived((counts?.marks ?? []).filter((m) => m.kind === 'hour'));
	function hourStep(step: 1 | -1): string | undefined {
		if (!zoom) return undefined;
		const i = hourMarks.findIndex((m) => m.start === zoom!.hour || ms(m.start) === ms(zoom!.hour));
		const j = i - step; // newest first: a later hour is before it
		return i >= 0 && j >= 0 && j < hourMarks.length ? hourMarks[j].start : undefined;
	}
	const later = $derived(hourStep(1));
	const earlier = $derived(hourStep(-1));

	function go(i: number) {
		const m = marks[Math.max(0, Math.min(marks.length - 1, i))];
		if (!m) return;
		picked = marks.indexOf(m);
		onjump(m.end);
	}

	// The keyboard moves the mark at once and jumps after a moment, so holding
	// an arrow key doesn't ask the API for every mark it passes.
	let keyTimer: ReturnType<typeof setTimeout> | undefined;
	function key(e: KeyboardEvent) {
		if (marks.length === 0) return;
		const from = shown ?? 0;
		let to: number | undefined;
		const byTime = (days: number) => {
			const t = ms(marks[from].start) + days * 864e5;
			const i = marks.findIndex((m) => ms(m.start) <= t && t < ms(m.end));
			return i >= 0 ? i : days > 0 ? 0 : marks.length - 1;
		};
		switch (e.key) {
			case 'ArrowUp':
			case 'ArrowRight':
				to = from - 1;
				break;
			case 'ArrowDown':
			case 'ArrowLeft':
				to = from + 1;
				break;
			case 'PageUp':
				to = byTime(7);
				break;
			case 'PageDown':
				to = byTime(-7);
				break;
			case 'Home':
				to = 0;
				break;
			case 'End':
				to = marks.length - 1;
				break;
			default:
				return;
		}
		e.preventDefault();
		picked = Math.max(0, Math.min(marks.length - 1, to));
		clearTimeout(keyTimer);
		const i = picked;
		keyTimer = setTimeout(() => go(i), 350);
	}

	function indexAt(y: number): number | undefined {
		if (!track || marks.length === 0) return undefined;
		const r = track.getBoundingClientRect();
		return Math.max(0, Math.min(marks.length - 1, Math.floor(((y - r.top) / r.height) * marks.length)));
	}
	function down(e: PointerEvent) {
		if (e.button !== 0 || marks.length === 0) return;
		track?.setPointerCapture(e.pointerId);
		track?.focus({ preventScroll: true });
		dragging = true;
		hover = indexAt(e.clientY);
		e.preventDefault();
	}
	function move(e: PointerEvent) {
		if (dragging) hover = indexAt(e.clientY);
	}
	function up(e: PointerEvent) {
		if (!dragging) return;
		dragging = false;
		const i = indexAt(e.clientY);
		hover = undefined;
		if (i !== undefined) go(i);
	}

	const value = $derived(shown === undefined ? 0 : marks.length - 1 - shown);
</script>

<div class="scrubber" class:zoomed={!!zoom} data-testid="scrubber" data-counted={counted}>
	<!-- svelte-ignore a11y_no_static_element_interactions (Escape hides the tooltips: WCAG 1.4.13) -->
	<div class="tools" onkeydown={(e) => e.key === 'Escape' && (tipOff = true)} onpointerleave={() => (tipOff = false)} onfocusout={() => (tipOff = false)}>
		{#if zoom}
			<span class="has-tip" class:tip-off={tipOff}>
				<button type="button" class="ghost icon-button" aria-label="Back to the days" onclick={() => (zoom = undefined)}><Icon name="zoomout" /></button>
				<span class="tooltip" aria-hidden="true">Back to the days</span>
			</span>
			<span class="has-tip" class:tip-off={tipOff}>
				<button type="button" class="ghost icon-button" aria-label="The hour after" disabled={!later} onclick={() => later && loadZoom(later)}><Icon name="later" /></button>
				<span class="tooltip" aria-hidden="true">The hour after</span>
			</span>
		{:else if zoomable}
			<span class="has-tip" class:tip-off={tipOff}>
				<button type="button" class="ghost icon-button" aria-label="Zoom into {name(zoomable)}, by 5 minutes" onclick={() => loadZoom(zoomable.start)}
					><Icon name="zoomin" /></button
				>
				<span class="tooltip" aria-hidden="true">This hour by 5 minutes</span>
			</span>
		{/if}
	</div>
	<!-- The strip is one slider: its marks aren't each a stop for the keyboard. -->
	<div
		class="track"
		bind:this={track}
		role="slider"
		tabindex="0"
		aria-label={zoom ? 'Jump to 5 minutes of the hour' : 'Jump to a date'}
		aria-orientation="vertical"
		aria-valuemin={0}
		aria-valuemax={Math.max(0, marks.length - 1)}
		aria-valuenow={value}
		aria-valuetext={shown !== undefined && marks[shown] ? spoken(marks[shown]) : 'No marks'}
		onkeydown={key}
		onpointerdown={down}
		onpointermove={move}
		onpointerup={up}
		onpointercancel={() => ((dragging = false), (hover = undefined))}
	>
		{#each marks as m, i (m.start)}
			{@const l = label(m, i)}
			<div class="mark {m.kind}" class:current={i === shown} data-count={m.count} data-kind={m.kind} data-start={m.start}>
				{#if l}<span class="label" aria-hidden="true">{l}</span>{/if}
				<span class="dots" aria-hidden="true">{#each { length: dots(m) } as _, k (k)}<span class="dot"></span>{/each}</span>
			</div>
		{/each}
		{#if dragging && hover !== undefined && marks[hover]}
			<span class="bubble" aria-hidden="true" style:top="{((hover + 0.5) / marks.length) * 100}%">{spoken(marks[hover])}</span>
		{/if}
	</div>
	{#if zoom}
		<!-- svelte-ignore a11y_no_static_element_interactions (Escape hides the tooltips: WCAG 1.4.13) -->
	<div class="tools" onkeydown={(e) => e.key === 'Escape' && (tipOff = true)} onpointerleave={() => (tipOff = false)} onfocusout={() => (tipOff = false)}>
			<span class="has-tip" class:tip-off={tipOff}>
				<button type="button" class="ghost icon-button" aria-label="The hour before" disabled={!earlier} onclick={() => earlier && loadZoom(earlier)}
					><Icon name="earlier" /></button
				>
				<span class="tooltip" aria-hidden="true">The hour before</span>
			</span>
		</div>
	{/if}
	{#if !counted}<p class="visually-hidden">These marks aren't counted: this search over this range would take too long to count.</p>{/if}
	{#if error}<p class="visually-hidden" role="status">{error}</p>{/if}
</div>

<style>
	/* Down the right of the list, in view while the page scrolls under the top
	   bar; on a phone, above the bottom bar. */
	.scrubber {
		position: sticky;
		top: var(--topbar-height);
		display: flex;
		flex-direction: column;
		align-items: stretch;
		width: var(--scrubber-width);
		height: calc(var(--full-height) - var(--topbar-height));
		padding: var(--space-2xs) 0;
		border-left: var(--border-width) solid var(--border);
		background: var(--surface);
		user-select: none;
	}
	.tools {
		display: flex;
		flex-direction: column;
		align-items: center;
		min-height: var(--target);
	}
	.zoomed .tools:last-of-type {
		min-height: 0;
	}
	/* Tooltips open to the left, into the list, not off the screen. */
	.tools .tooltip {
		top: 50%;
		left: auto;
		right: calc(100% + var(--tip-gap));
		transform: translateY(-50%);
	}
	.track {
		position: relative;
		flex: 1;
		display: flex;
		flex-direction: column;
		min-height: 0;
		cursor: pointer;
		touch-action: none;
		border-radius: var(--radius-small);
	}
	.mark {
		position: relative;
		flex: 1 1 0;
		min-height: 0;
		display: flex;
		align-items: center;
		justify-content: flex-end;
		padding-right: var(--space-xs);
		border-right: var(--scrubber-edge) solid transparent;
	}
	.mark.current {
		background: var(--accent-soft);
		border-right-color: var(--accent);
	}
	.track:hover .mark:hover {
		background: var(--hover);
	}
	.track:hover .mark.current:hover {
		background: var(--accent-soft);
	}
	.label {
		position: absolute;
		left: var(--space-2xs);
		top: 50%;
		transform: translateY(-50%);
		color: var(--muted);
		font-size: var(--text-xxs);
		line-height: var(--leading-tight);
		white-space: nowrap;
		pointer-events: none;
	}
	.dots {
		display: flex;
		gap: var(--space-3xs);
		height: 100%;
		align-items: center;
	}
	.dot {
		width: var(--scrubber-dot);
		height: min(var(--scrubber-dot), 100%);
		border-radius: var(--radius-round);
		background: var(--scrubber-dot-colour);
	}
	.current .dot {
		background: var(--accent);
	}
	.bubble {
		position: absolute;
		right: calc(100% + var(--space-xs));
		transform: translateY(-50%);
		padding: var(--space-2xs) var(--space-sm);
		border-radius: var(--radius-small);
		background: var(--fg);
		color: var(--bg);
		font-size: var(--text-xs);
		white-space: nowrap;
		pointer-events: none;
		z-index: var(--layer-overlay);
	}
	.track:focus-visible {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: calc(-1 * var(--focus-width));
	}
	@media (max-width: 47.99rem) {
		.scrubber {
			width: calc(var(--scrubber-width-phone) + var(--border-width));
			height: calc(var(--full-height) - var(--topbar-height) - var(--bottom-bar-height));
		}
		.label {
			display: none;
		}
		.mark {
			justify-content: center;
			padding-right: 0;
		}
	}
	@media print {
		.scrubber {
			display: none;
		}
	}
</style>
