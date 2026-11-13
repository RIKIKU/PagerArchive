<script lang="ts">
	// R81b-R81e: the scrubber, a strip down the right of a message list for
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
	// focuses it, and a line across it follows the list as it scrolls. A touch
	// screen gets a grab tab instead (R81e, below). Albums and phone-width
	// windows with a mouse have none (MessageList).
	import { onMount, untrack } from 'svelte';
	import { api, type Counts, type Mark } from './api.ts';
	import Icon from './Icon.svelte';
	import { screen } from './screen.svelte.ts';
	import { ownEdge, scrubberHere } from './scrubberHere.svelte.ts';

	let {
		query,
		tz,
		at,
		host,
		onjump,
		onleave,
		touch = false
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
		/** Escape: the keyboard goes back to the list. */
		onleave?: () => void;
		/** A touch screen (no hover, or a coarse pointer): a grab tab that
		    shows while the list scrolls, instead of the strip of marks. */
		touch?: boolean;
	} = $props();

	// How near the window's edge, in px, the pointer must come to show the
	// strip (owner, 2026-11-13: nearer hits the browser's scroll bar).
	const ZONE = 50;

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
			want = done = undefined;
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

	// Jumps go one at a time, to the latest mark wanted, at most one every
	// 120 ms: a drag asks for many marks, and only the latest matters.
	// The ends of the marks (or, on a touch screen, the steps) wanted and last
	// jumped to: a time, so either can ask.
	let want: string | undefined;
	let done: string | undefined;
	const endOf = (i: number | undefined) => (i === undefined ? undefined : marks[i]?.end);
	let pumping: Promise<void> | undefined;
	function pump(): Promise<void> {
		// .finally runs after the assignment even when there's nothing to do.
		pumping ??= (async () => {
			while (want !== undefined && want !== done) {
				const j = want;
				done = j;
				const t0 = performance.now();
				await onjump(j);
				const rest = 120 - (performance.now() - t0);
				if (rest > 0 && want !== j) await new Promise((r) => setTimeout(r, rest));
			}
		})().finally(() => (pumping = undefined));
		return pumping;
	}

	function go(i: number) {
		const m = marks[Math.max(0, Math.min(marks.length - 1, i))];
		if (!m) return;
		picked = marks.indexOf(m);
		landed = undefined;
		want = marks[picked].end;
		done = undefined;
		pump().then(() => (landed = current ?? -1));
	}

	// The keyboard moves the mark at once and jumps after a moment, so holding
	// an arrow key doesn't ask the API for every mark it passes.
	let keyTimer: ReturnType<typeof setTimeout> | undefined;
	function key(e: KeyboardEvent) {
		// Escape: back to the list, at the row at the top of the view.
		if (e.key === 'Escape') {
			e.preventDefault();
			onleave?.();
			return;
		}
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
	// A press jumps to the mark under the pointer; holding the button and
	// dragging scrubs: the list follows each new mark under the pointer as
	// it goes (owner, 2026-11-13), not only on release. The pointer is
	// captured, so the drag goes on (and the strip stays shown) when it
	// leaves the strip, until the button is let go.
	function down(e: PointerEvent) {
		if (e.button !== 0 || marks.length === 0) return;
		track?.setPointerCapture(e.pointerId);
		track?.focus({ preventScroll: true });
		dragging = true;
		hover = indexAt(e.clientY);
		picked = undefined;
		landed = undefined;
		want = endOf(hover);
		done = undefined;
		pump();
		e.preventDefault();
	}
	function move(e: PointerEvent) {
		if (!dragging) return;
		const i = indexAt(e.clientY);
		if (i === hover) return;
		hover = i;
		want = endOf(i);
		pump();
	}
	function up(e: PointerEvent) {
		if (!dragging) return;
		const i = indexAt(e.clientY) ?? hover;
		dragging = false;
		measure();
		hover = undefined;
		if (i === undefined) return;
		picked = i;
		landed = undefined;
		want = endOf(i);
		pump().then(() => (landed = current ?? -1));
	}
	function cancel() {
		dragging = false;
		measure();
		hover = undefined;
	}

	// Alt+J (Option+J on a Mac) from anywhere on the page, or the skip link,
	// brings the strip out with the keyboard on it (owner, 2026-11-13: Tab
	// walks the whole side menu first).
	function focusStrip() {
		focused = true;
		track?.focus({ preventScroll: true });
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
		// Not while dragging: the marks stay under the pointer as the list moves.
		if (!dragging) top = Math.round(Math.max(bar, r.top));
		// Only a page's padding beside it (and the room the hidden scroll bar
		// had): the strip and its zone own the window's edge (owner,
		// 2026-11-13). A message open beside it (R80): they're at the list's.
		const beside = width - r.right < 64;
		right = beside ? 0 : Math.round(width - r.right);
		edge = beside ? width : r.right;
		outer = beside ? Infinity : r.right + 4;
		// Touch: the rail's bottom and the tab's height, for how far the tab
		// can move (travel; a drag keeps the one it started with).
		if (touch && root) {
			railBottom = root.getBoundingClientRect().bottom;
			tabHeight = track?.offsetHeight || 48;
		}
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
			near = e.clientX >= edge - ZONE && e.clientX <= outer && e.clientY >= top;
		};
		const leave = () => (near = false);
		window.addEventListener('pointermove', move, { passive: true });
		document.documentElement.addEventListener('pointerleave', leave);
		// e.code, so Option+J on a Mac (which types a letter) is J too.
		const shortcut = (e: KeyboardEvent) => {
			if (e.code !== 'KeyJ' || !e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
			e.preventDefault();
			focusStrip();
		};
		window.addEventListener('keydown', shortcut);
		scrubberHere.focus = focusStrip;
		const release = ownEdge();
		return () => {
			release();
			window.removeEventListener('keydown', shortcut);
			if (scrubberHere.focus === focusStrip) scrubberHere.focus = undefined;
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

	// Touch screens (owner, 2026-11-13): a fast-scroll tab, as photo apps
	// have. Nothing until the list scrolls; then a tab at the right edge, at
	// the list's place in time, which fades a moment after the scrolling
	// stops. Dragging it scrubs, through the same jumps as a drag on the
	// strip, with a bubble naming the step the drag will land on. No dots.
	//
	// The rail is steps a thumb can hold: whole days (today and yesterday
	// aren't split into hours here), or, when a day would get less than
	// MIN_STEP px of the rail, weeks, then months, then years. During a drag
	// the tab is under the finger, the finger's place on the rail picks the
	// step through a mapping fixed when the drag starts (the list loading
	// rows, or the browser's bars coming and going, don't change it), and a
	// step changes only once the finger is well into the next one, so the
	// day doesn't flicker at a boundary. The list follows the step, never the
	// other way round.
	const MIN_STEP = 16; // px of the rail per step
	const HOLD = 0.3; // of a step, past its edge, before the next is picked
	type Step = { start: number; end: number; endIso: string; label: string };
	type Level = 'day' | 'week' | 'month' | 'year';
	let scrolling = $state(false);
	let scrollTimer: ReturnType<typeof setTimeout> | undefined;
	// px the tab can move: the rail, from its top (as just set) to its bottom
	// (measured; it doesn't move with the top), less the tab.
	let railBottom = $state(0);
	let tabHeight = $state(0);
	const travel = $derived(Math.max(0, Math.round(railBottom - top - tabHeight)));
	const railShown = $derived(scrolling || dragging || focused);
	$effect(() => {
		if (!touch) return;
		const scrolled = () => {
			scrolling = true;
			clearTimeout(scrollTimer);
			scrollTimer = setTimeout(() => (scrolling = false), 1500);
		};
		window.addEventListener('scroll', scrolled, { passive: true });
		return () => {
			clearTimeout(scrollTimer);
			window.removeEventListener('scroll', scrolled);
		};
	});
	// The tab's own height, once it's drawn.
	$effect(() => {
		if (!touch) return;
		void steps.length;
		const f = requestAnimationFrame(measure);
		return () => cancelAnimationFrame(f);
	});
	const weekdays = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
	const monday = (t: number) => t + 432e5 - weekdays.indexOf(fmt({ weekday: 'short' }).format(new Date(t))) * 864e5;
	function keyOf(level: Level, t: number): string {
		const d = new Date(t);
		switch (level) {
			case 'day':
				return dayKey(d.toISOString());
			case 'week':
				return dayKey(new Date(monday(t)).toISOString());
			case 'month':
				return fmt({ year: 'numeric', month: '2-digit' }).format(d);
			case 'year':
				return fmt({ year: 'numeric' }).format(d);
		}
	}
	function labelOf(level: Level, t: number): string {
		const d = new Date(t);
		switch (level) {
			case 'day':
				return whichDay(d.toISOString());
			case 'week':
				return `Week of ${fmt({ day: 'numeric', month: 'short' }).format(new Date(monday(t)))}`;
			case 'month':
				return fmt({ month: 'long', year: 'numeric' }).format(d);
			case 'year':
				return fmt({ year: 'numeric' }).format(d);
		}
	}
	function stepsOf(level: Level): Step[] {
		const out: Step[] = [];
		let key = '';
		for (const m of counts?.marks ?? []) {
			const k = keyOf(level, ms(m.start));
			if (k === key) out[out.length - 1].start = ms(m.start);
			else out.push({ start: ms(m.start), end: ms(m.end), endIso: m.end, label: '' });
			key = k;
		}
		for (const s of out) s.label = labelOf(level, s.start);
		return out;
	}
	// The finest level whose steps get MIN_STEP px each; nothing finer than
	// the marks themselves (weeks over 90 days, months over a year).
	const steps = $derived.by(() => {
		const marked = counts?.marks ?? [];
		if (marked.length === 0 || !touch) return [];
		// A phone: the last 7 days, unless a range is chosen (owner, 2026-11-13:
		// "30 days is a long time for a phone sized screen"). Only the rail: the
		// list scrolls on past them, with the tab at the bottom.
		const ranged = /(^|&)(from|to)=/.test(query);
		if (screen.phone && !ranged) return stepsOf('day').slice(0, 7);
		const kinds = new Set(marked.map((m) => m.kind));
		const levels: Level[] = kinds.has('month') ? ['month', 'year'] : kinds.has('week') ? ['week', 'month', 'year'] : ['day', 'week', 'month', 'year'];
		let out: Step[] = [];
		for (const level of levels) {
			out = stepsOf(level);
			if (travel <= 0 || travel / out.length >= MIN_STEP) break;
		}
		return out;
	});
	// Where the list is, on the rail (0 to 1).
	function placeOf(list: Step[], t: number | undefined): number {
		if (list.length === 0 || t === undefined || t >= list[0].end) return 0;
		const i = list.findIndex((s) => s.start <= t && t < s.end);
		if (i < 0) return 1;
		return (i + (list[i].end - t) / (list[i].end - list[i].start)) / list.length;
	}
	// While dragging: the steps and the px per step fixed at the start, the
	// finger's place, and the step picked.
	let drag: { steps: Step[]; travel: number; y: number; at: number } | undefined = $state.raw();
	let fingerAt = $state(0);
	let chosen: number | undefined = $state();
	const restAt = $derived(placeOf(steps, at));
	const tabTop = $derived(dragging && drag ? fingerAt * drag.travel : restAt * travel);
	const chosenStep = $derived(dragging && drag && chosen !== undefined ? drag.steps[chosen] : undefined);
	// The list scrolled on past the rail (older than a phone's 7 days, say):
	// the tab waits at the bottom and says where the list is.
	const beyond = $derived(at !== undefined && steps.length > 0 && at < steps[steps.length - 1].start);
	const pastRail = $derived(beyond && at !== undefined ? `Earlier: ${whichDay(new Date(at).toISOString())}` : undefined);
	const restStep = $derived(beyond ? steps[steps.length - 1] : (steps.find((s) => at !== undefined && s.start <= at && at < s.end) ?? steps[0]));
	function tabDown(e: PointerEvent) {
		if (steps.length === 0 || !root || (e.pointerType === 'mouse' && e.button !== 0)) return;
		try {
			(e.currentTarget as Element).setPointerCapture(e.pointerId);
		} catch {
			// a pointer the browser doesn't know (a test's): the events still come here
		}
		drag = { steps, travel: Math.max(1, travel), y: e.clientY, at: restAt };
		fingerAt = restAt;
		chosen = Math.min(steps.length - 1, Math.floor(restAt * steps.length));
		dragging = true;
		picked = undefined;
		landed = undefined;
		want = done = steps[chosen].endIso; // touching it doesn't jump; moving it does
		e.preventDefault();
	}
	function tabMove(e: PointerEvent) {
		if (!dragging || !drag || chosen === undefined) return;
		fingerAt = Math.max(0, Math.min(1, drag.at + (e.clientY - drag.y) / drag.travel));
		const raw = fingerAt * drag.steps.length;
		if (raw >= chosen - HOLD && raw < chosen + 1 + HOLD) return;
		chosen = Math.max(0, Math.min(drag.steps.length - 1, Math.floor(raw)));
		want = drag.steps[chosen].endIso;
		pump();
	}
	function tabUp() {
		if (!dragging) return;
		const step = chosenStep;
		dragging = false;
		drag = undefined;
		chosen = undefined;
		measure();
		if (step) {
			want = step.endIso;
			pump();
		}
		// It fades as after a scroll.
		scrolling = true;
		clearTimeout(scrollTimer);
		scrollTimer = setTimeout(() => (scrolling = false), 1500);
	}
	// The keyboard on the tab: a step at a time, Home and End to the ends.
	function tabKey(e: KeyboardEvent) {
		if (e.key === 'Escape') return key(e);
		const i = restStep ? steps.indexOf(restStep) : 0;
		const to = { ArrowUp: i - 1, ArrowRight: i - 1, ArrowDown: i + 1, ArrowLeft: i + 1, Home: 0, End: steps.length - 1 }[e.key];
		if (to === undefined || steps.length === 0) return;
		e.preventDefault();
		want = steps[Math.max(0, Math.min(steps.length - 1, to))].endIso;
		done = undefined;
		pump();
	}
</script>

{#if touch}
	<!-- svelte-ignore a11y_no_static_element_interactions (the tab within is the control) -->
	<div
		class="rail"
		class:shown={railShown}
		bind:this={root}
		style:top="{top}px"
		style:right="{right}px"
		data-testid="scrubber"
		data-touch="true"
		data-shown={railShown}
		onfocusin={focusIn}
		onfocusout={focusOut}
		onkeydown={(e) => e.key !== 'Escape' && (focused = true)}
	>
		{#if steps.length > 0}
			{@const showing = chosenStep ?? restStep}
			<div class="tab-place" style:top="{tabTop}px" data-steps={steps.length}>
				{#if chosenStep}<span class="tab-bubble" class:low={tabTop < 48} aria-hidden="true">{chosenStep.label}</span>
				{:else if pastRail && railShown}<span class="tab-bubble quiet" aria-hidden="true">{pastRail}</span>{/if}
				<div
					class="tab"
					class:held={dragging}
					bind:this={track}
					role="slider"
					tabindex="0"
					aria-label="Jump to a date"
					aria-orientation="vertical"
					aria-keyshortcuts="Alt+J"
					aria-valuemin={0}
					aria-valuemax={steps.length - 1}
					aria-valuenow={showing ? steps.length - 1 - Math.max(0, (drag?.steps ?? steps).indexOf(showing)) : 0}
					aria-valuetext={!chosenStep && pastRail ? pastRail : (showing?.label ?? 'No dates')}
					onkeydown={tabKey}
					onpointerdown={tabDown}
					onpointermove={tabMove}
					onpointerup={tabUp}
					onpointercancel={tabUp}
				>
					<span class="grip" aria-hidden="true"></span>
				</div>
			</div>
		{/if}
	</div>
{:else}
<!-- svelte-ignore a11y_no_static_element_interactions (shows it while the pointer is over it, or a key is pressed in it) -->
<div
	class="scrubber"
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
	onkeydown={(e) => e.key !== 'Escape' && (focused = true)}
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
		aria-keyshortcuts="Alt+J"
		onkeydown={key}
		onpointerdown={down}
		onpointermove={move}
		onpointerup={up}
		onpointercancel={cancel}
		onlostpointercapture={() => dragging && cancel()}
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
{/if}

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
	/* To the left of the strip, over the list, with nothing behind it but a
	   halo to be read by (owner, 2026-11-13): let clicks through. */
	.label {
		position: absolute;
		right: calc(100% + var(--space-xs));
		top: 50%;
		transform: translateY(-50%);
		color: var(--fg);
		text-shadow: var(--scrubber-halo);
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
	/* Touch: the rail down the right edge holds the grab tab; only the tab
	   takes a finger, and only while it shows. On a phone, above the bottom
	   bar. */
	.rail {
		position: fixed;
		bottom: 0;
		z-index: var(--layer-content);
		width: max(var(--target), var(--scrubber-tab-width-held));
		pointer-events: none;
		opacity: 0;
		transition: opacity 400ms ease-in;
	}
	.rail.shown {
		opacity: 1;
		transition: opacity 120ms ease-out;
	}
	.rail.shown .tab {
		pointer-events: auto;
	}
	.tab-place {
		position: absolute;
		right: 0;
		display: flex;
		align-items: center;
	}
	.tab {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		width: max(var(--target), var(--scrubber-tab-width-held));
		height: var(--scrubber-tab-height);
		touch-action: none;
		cursor: grab;
		user-select: none;
	}
	.grip {
		display: flex;
		align-items: center;
		justify-content: center;
		width: var(--scrubber-tab-width);
		height: 100%;
		border-radius: var(--radius) 0 0 var(--radius); /* against the edge */
		background: var(--accent);
		box-shadow: var(--scrubber-tab-shadow);
	}
	/* Two short lines across it: something to take hold of. */
	.grip::before {
		content: '';
		width: 45%;
		height: var(--scrubber-grip-gap);
		border-top: var(--border-width-strong) solid var(--accent-fg);
		border-bottom: var(--border-width-strong) solid var(--accent-fg);
	}
	.tab.held .grip {
		width: var(--scrubber-tab-width-held);
	}
	.tab:focus-visible {
		outline: none;
	}
	.tab:focus-visible .grip {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: var(--focus-offset);
	}
	/* Up and to the left of the tab, clear of the finger on it; below it near
	   the top, so it stays in view. */
	.tab-bubble {
		position: absolute;
		right: calc(100% + var(--space-sm));
		bottom: calc(100% - var(--space-xs));
		padding: var(--space-xs) var(--space-md);
		border-radius: var(--radius-small);
		background: var(--fg);
		color: var(--bg);
		font-size: var(--text-md);
		font-weight: var(--weight-strong);
		white-space: nowrap;
		pointer-events: none;
	}
	/* Past the rail: where the list is, quietly, while the tab shows. */
	.tab-bubble.quiet {
		bottom: auto;
		top: 50%;
		transform: translateY(-50%);
		border: var(--border-width) solid var(--border);
		background: var(--surface);
		color: var(--fg);
		font-size: var(--text-sm);
		font-weight: var(--weight-normal);
	}
	.tab-bubble.low {
		top: calc(100% - var(--space-xs));
		bottom: auto;
	}
	@media (max-width: 47.99rem) {
		.rail {
			bottom: calc(var(--bottom-bar-height) + env(safe-area-inset-bottom));
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.scrubber,
		.hideable:not(.shown),
		.rail,
		.rail.shown {
			transition: none;
		}
	}
	@media print {
		.scrubber,
		.rail {
			display: none;
		}
	}
</style>
