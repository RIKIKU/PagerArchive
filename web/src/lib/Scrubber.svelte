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
	//
	// It sits over the list's right edge, not in a column of its own. With a
	// mouse it stays hidden until the pointer nears that edge or the keyboard
	// focuses it, and a line across it follows the list as it scrolls. Phones
	// and albums have none (MessageList).
	import { onMount, untrack } from 'svelte';
	import { api, type Counts, type Mark } from './api.ts';
	import Icon from './Icon.svelte';

	let {
		query,
		tz,
		at,
		host,
		onjump
	}: {
		/** The list's API parameters, as a string (MessageList's askedKey). */
		query: string;
		tz: string;
		/** Where the top of the list's view is, in time (ms): the first row in
		    view's sent time, moving towards the next row's as it scrolls past. */
		at?: number;
		/** The list's own box: the strip sits over its right edge. */
		host?: HTMLElement;
		/** Jump the list to the messages sent before this time. */
		onjump: (before: string) => Promise<void>;
	} = $props();

	// POC (2026-11-13): the choices to compare, from the address once
	// (?zone=3, ?zone=12, ?hint=0|1|2, ?bg=veil|clear|solid) and kept for
	// the tab, so they last as the address changes.
	// zone: how near the edge, in px, the pointer must come to show it;
	// hint: 0 nothing while hidden, 1 a faint line down the edge, 2 the line
	// and a tick on it where the list is; bg: veil (see-through, blurred),
	// clear (nothing behind it, text with a halo) or solid.
	const poc = (() => {
		const out = { zone: 12, hint: 1, bg: 'veil' };
		if (typeof window === 'undefined') return out;
		try {
			Object.assign(out, JSON.parse(sessionStorage.getItem('scrubber-poc') ?? '{}'));
		} catch {
			// no storage: the defaults and the address
		}
		const q = new URLSearchParams(location.search);
		const zone = Number(q.get('zone'));
		if (q.has('zone') && zone >= 1 && zone <= 64) out.zone = zone;
		const hint = Number(q.get('hint'));
		if (q.has('hint') && [0, 1, 2].includes(hint)) out.hint = hint;
		const bg = q.get('bg');
		if (bg === 'veil' || bg === 'clear' || bg === 'solid') out.bg = bg;
		try {
			sessionStorage.setItem('scrubber-poc', JSON.stringify(out));
		} catch {
			// ignore
		}
		return out;
	})();

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
		if (at === undefined) return 0;
		const t = at;
		const i = marks.findIndex((m) => ms(m.start) <= t && t < ms(m.end));
		if (i >= 0) return i;
		return t >= ms(marks[0].end) ? 0 : t < ms(marks[marks.length - 1].start) ? marks.length - 1 : undefined;
	});
	const shown = $derived(dragging && hover !== undefined ? hover : (picked ?? current));

	// Where the list is, as a fraction of the strip's height, moving
	// smoothly as it scrolls: through its mark in proportion to the time.
	const thumb = $derived.by(() => {
		if (marks.length === 0) return undefined;
		if (at === undefined) return 0;
		const t = at;
		if (t >= ms(marks[0].end)) return 0;
		const i = marks.findIndex((m) => ms(m.start) <= t && t < ms(m.end));
		if (i < 0) return t < ms(marks[marks.length - 1].start) ? 1 : undefined;
		const m = marks[i];
		return (i + (ms(m.end) - t) / (ms(m.end) - ms(m.start))) / marks.length;
	});

	// A mark the keyboard or a click picked stays picked while the list gets
	// there (an empty mark's jump lands in an older one), and until it's
	// scrolled to another mark: then the strip follows the list again.
	let landed: number | undefined;
	$effect(() => {
		const c = current;
		untrack(() => {
			if (picked === undefined || landed === undefined || dragging || keyTimer !== undefined) return;
			if (c !== landed) {
				picked = undefined;
				landed = undefined;
			}
		});
	});

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
				return fmt({ weekday: 'short' }).format(d) === 'Mon' ? fmt({ day: 'numeric', month: 'short' }).format(d) : '';
			}
			case 'week': {
				const month = fmt({ month: 'short' }).format(new Date(m.start));
				return prev?.kind === 'week' && fmt({ month: 'short' }).format(new Date(prev.start)) !== month ? month : '';
			}
			case 'month': {
				const d = new Date(m.start);
				const month = fmt({ month: 'numeric' }).format(d);
				return month === '1' ? fmt({ year: 'numeric' }).format(d) : '';
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
		landed = undefined;
		onjump(m.end).then(() => (landed = current ?? -1));
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
		keyTimer = setTimeout(() => {
			keyTimer = undefined;
			go(i);
		}, 350);
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

	// Over the list's right edge, from the top bar (or the list's top, below
	// it) to the bottom of the window. Measured as the page scrolls or
	// changes size; position: fixed, so loading more rows never moves it.
	let top = $state(0);
	let right = $state(0);
	let edge = 0; // the list's right edge, from the window's left
	let outer = 0; // how far right the edge zone reaches
	function measure() {
		if (!host) return;
		const r = host.getBoundingClientRect();
		const width = document.documentElement.clientWidth;
		const bar = document.querySelector('header.top')?.getBoundingClientRect().bottom ?? 0;
		top = Math.round(Math.max(bar, r.top));
		right = Math.round(Math.max(0, width - r.right));
		edge = r.right;
		// Only a page's padding beside it: the zone runs to the window's edge.
		// A message open beside it (R80): the zone stops at the list's edge.
		outer = width - r.right < 64 ? width : r.right + 4;
	}

	// Hidden until wanted, where there's a mouse to want it with. Shown at
	// once; hidden a moment after the pointer leaves, so it doesn't flicker.
	let hideable = $state(false);
	let near = $state(false);
	let over = $state(false);
	let focused = $state(false);
	let visible = $state(false);
	let root: HTMLElement | undefined = $state();
	const wanted = $derived(near || over || focused || dragging);
	let hideTimer: ReturnType<typeof setTimeout> | undefined;
	$effect(() => {
		if (wanted) {
			clearTimeout(hideTimer);
			visible = true;
		} else hideTimer = setTimeout(() => (visible = false), 400);
		return () => clearTimeout(hideTimer);
	});
	onMount(() => {
		const fine = window.matchMedia('(hover: hover) and (pointer: fine)');
		hideable = fine.matches;
		const change = (e: MediaQueryListEvent) => (hideable = e.matches);
		fine.addEventListener('change', change);
		let frame = 0;
		const later = () => {
			if (!frame)
				frame = requestAnimationFrame(() => {
					frame = 0;
					measure();
				});
		};
		measure();
		const ro = new ResizeObserver(later);
		if (host) ro.observe(host);
		window.addEventListener('scroll', later, { passive: true });
		window.addEventListener('resize', later, { passive: true });
		const move = (e: PointerEvent) => {
			if (e.pointerType === 'touch') return;
			near = e.clientX >= edge - poc.zone && e.clientX <= outer && e.clientY >= top;
		};
		const leave = () => (near = false);
		window.addEventListener('pointermove', move, { passive: true });
		document.documentElement.addEventListener('pointerleave', leave);
		return () => {
			fine.removeEventListener('change', change);
			cancelAnimationFrame(frame);
			ro.disconnect();
			window.removeEventListener('scroll', later);
			window.removeEventListener('resize', later);
			window.removeEventListener('pointermove', move);
			document.documentElement.removeEventListener('pointerleave', leave);
		};
	});
	// The keyboard shows it (it's a slider for the keyboard too, R81b); a
	// click doesn't keep it shown.
	function focusIn(e: FocusEvent) {
		if ((e.target as Element).matches(':focus-visible')) focused = true;
	}
	function focusOut(e: FocusEvent) {
		if (!root?.contains(e.relatedTarget as Node | null)) focused = false;
	}
</script>

{#if hideable && poc.hint > 0}
	<!-- POC: a faint line down the edge while it's hidden, so it can be found; with hint=2 a tick where the list is. -->
	<div class="hint" class:off={visible} style:top="{top}px" style:right="{right}px" aria-hidden="true">
		{#if poc.hint === 2 && thumb !== undefined}<span class="tick" style:top="{thumb * 100}%"></span>{/if}
	</div>
{/if}
<!-- svelte-ignore a11y_no_static_element_interactions (shows it while the pointer is over it, or a key is pressed in it) -->
<div
	class="scrubber bg-{poc.bg}"
	class:zoomed={!!zoom}
	class:hideable
	class:shown={visible || !hideable}
	bind:this={root}
	style:top="{top}px"
	style:right="{right}px"
	data-testid="scrubber"
	data-counted={counted}
	data-shown={visible || !hideable}
	onpointerenter={() => (over = true)}
	onpointerleave={() => (over = false)}
	onfocusin={focusIn}
	onfocusout={focusOut}
	onkeydown={() => (focused = true)}
>
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
		{#if thumb !== undefined}<span class="thumb" style:top="{thumb * 100}%" aria-hidden="true"></span>{/if}
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
	/* Over the right edge of the list (fixed: measured in the script), from
	   the top bar to the bottom of the window. Only the strip of dots on its
	   right takes the pointer, as wide as a target; the labels to its left let
	   clicks through to the rows, whose controls stay clear of the strip
	   (MessageList's --row-pad-end). */
	.scrubber {
		position: fixed;
		bottom: 0;
		z-index: var(--layer-content);
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		width: var(--scrubber-width);
		padding: var(--space-2xs) 0;
		user-select: none;
		pointer-events: none;
		transition: opacity 120ms ease-out;
	}
	.bg-veil {
		background: var(--scrubber-veil);
		backdrop-filter: blur(var(--scrubber-blur));
		border-left: var(--border-width) solid var(--border);
	}
	.bg-solid {
		background: var(--surface);
		border-left: var(--border-width) solid var(--border);
	}
	.bg-clear .label {
		color: var(--fg);
		text-shadow: var(--scrubber-halo);
	}
	.hideable:not(.shown) {
		opacity: 0;
		transition: opacity 200ms ease-in;
	}
	.hideable:not(.shown) .tools,
	.hideable:not(.shown) .track {
		pointer-events: none;
	}
	.tools,
	.track {
		pointer-events: auto;
	}
	.hint {
		position: fixed;
		bottom: 0;
		width: var(--scrubber-hint-width);
		background: var(--scrubber-hint);
		pointer-events: none;
		transition: opacity 200ms;
	}
	.hint.off {
		opacity: 0;
	}
	.tick {
		position: absolute;
		right: 0;
		width: var(--scrubber-tick);
		height: var(--scrubber-hint-width);
		background: var(--accent);
	}
	.tools {
		display: flex;
		flex-direction: column;
		align-items: center;
		width: var(--scrubber-hit);
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
		width: var(--scrubber-hit);
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
	/* To the left of the strip, over the list: let clicks through. */
	.label {
		position: absolute;
		right: calc(100% + var(--space-xs));
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
	/* Where the list is, following it as it scrolls. */
	.thumb {
		position: absolute;
		right: 0;
		left: 0;
		height: var(--scrubber-thumb);
		transform: translateY(-50%);
		background: var(--accent);
		pointer-events: none;
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
	@media (prefers-reduced-motion: reduce) {
		.scrubber,
		.hideable:not(.shown),
		.hint {
			transition: none;
		}
	}
	@media print {
		.scrubber,
		.hint {
			display: none;
		}
	}
</style>
