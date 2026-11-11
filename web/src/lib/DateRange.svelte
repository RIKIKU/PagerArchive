<script lang="ts">
	// R33a's Dates (M21): a calendar icon opens one month's calendar. The
	// first day picked is From and the second To, with the days between
	// joined by a bar, previewed as the pointer or the focus moves. A second
	// day before the first swaps them, the same day twice is one day, and a
	// third pick starts again. Whole days, in the deployment's time zone; no
	// day after today. The range shows as text beside the icon. Keyboard:
	// arrow keys move the day (Page Up and Page Down the month, Home and End
	// the week), Enter picks, Escape closes.
	//
	// The month-and-year title is a button: it switches to that year's 12
	// months, whose title (the year) switches to a grid of years. Picking a
	// year shows its months, and picking a month its days. Years with no pages
	// (outside the archive's first and last pages, firstPage and lastPage) and
	// months after this one are greyed out and can't be picked. The keys work
	// in each grid as in the days (arrows move, Page Up and Page Down a year or
	// 12 years, Enter picks, Escape closes).
	//
	// The range is in the form as From and To (hidden fields), so a GET form
	// sends it (the capcode report); onchange is told when the calendar
	// closes with a different range (the search box searches then).
	import { tick } from 'svelte';
	import Icon from './Icon.svelte';
	import { isoDate } from './format.ts';

	let {
		from = '',
		to = '',
		timeZone,
		firstPage,
		lastPage,
		onchange
	}: {
		from?: string;
		to?: string;
		timeZone: string;
		/** The days of the archive's first and last pages: the years with pages. */
		firstPage?: string;
		lastPage?: string;
		onchange?: (from: string, to: string) => void;
	} = $props();

	const uid = $props.id();
	// Follow the range given (a new search, Clear all, Back), and what's picked.
	let start = $derived(from);
	let end = $derived(to || from);
	let open = $state(false);
	let pending = $state(false);
	let hover: string | undefined = $state();
	let focusDay = $state('');
	let view = $state({ y: 0, m: 0 });
	let tipOff = $state(false);
	let button: HTMLButtonElement;
	let wrap: HTMLDivElement;
	let grid: HTMLDivElement | undefined = $state();
	// What the calendar shows: one month's days, one year's months, or 12
	// years. In the months and years, view is the one focused.
	let mode = $state<'days' | 'months' | 'years'>('days');

	// Dates are YYYY-MM-DD, worked on as UTC days so the browser's own time
	// zone never moves them.
	const day = (iso: string) => {
		const [y, m, d] = iso.split('-').map(Number);
		return new Date(Date.UTC(y, m - 1, d));
	};
	const iso = (d: Date) => d.toISOString().slice(0, 10);
	const addDays = (s: string, n: number) => {
		const d = day(s);
		d.setUTCDate(d.getUTCDate() + n);
		return iso(d);
	};
	const addMonths = (s: string, n: number) => {
		const d = day(s);
		const target = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + n, 1));
		const last = new Date(Date.UTC(target.getUTCFullYear(), target.getUTCMonth() + 1, 0)).getUTCDate();
		target.setUTCDate(Math.min(d.getUTCDate(), last));
		return iso(target);
	};
	const weekday = (s: string) => (day(s).getUTCDay() + 6) % 7; // Monday first
	const format = (opts: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('en-AU', { timeZone: 'UTC', ...opts });
	const long = format({ weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
	const monthName = format({ month: 'long', year: 'numeric' });

	const today = $derived(isoDate(new Date().toISOString(), timeZone));
	const short = (s: string) => {
		const d = day(s);
		return format(s.slice(0, 4) === today.slice(0, 4) ? { day: 'numeric', month: 'short' } : { day: 'numeric', month: 'short', year: 'numeric' }).format(d);
	};

	const label = $derived(!start ? '' : pending ? `${short(start)} – …` : start === end ? short(start) : `${short(start)} – ${short(end)}`);

	// The range drawn: the one picked, or while picking, the one previewed.
	const drawn = $derived.by(() => {
		if (!start) return undefined;
		if (pending) {
			const h = hover ?? start;
			return h < start ? { a: h, b: start } : { a: start, b: h };
		}
		return { a: start, b: end };
	});

	const weeks = $derived.by(() => {
		const first = iso(new Date(Date.UTC(view.y, view.m, 1)));
		const monday = addDays(first, -weekday(first));
		return Array.from({ length: 6 }, (_, w) => Array.from({ length: 7 }, (_, i) => addDays(monday, w * 7 + i)));
	});
	const inView = (s: string) => Number(s.slice(0, 4)) === view.y && Number(s.slice(5, 7)) - 1 === view.m;
	const hint = $derived(mode === 'months' ? 'Pick a month.' : mode === 'years' ? 'Pick a year.' : pending ? 'Now pick the last day.' : start ? 'Pick a day to start again.' : 'Pick the first day.');

	// The months and the years (the title's button switches to them).
	const monthShort = format({ month: 'short' });
	const yearsFrom = $derived(view.y - (((view.y % 12) + 12) % 12));
	const years = $derived(Array.from({ length: 12 }, (_, i) => yearsFrom + i));
	const thisMonth = $derived(today.slice(0, 7));
	const ym = (y: number, m: number) => `${y}-${String(m + 1).padStart(2, '0')}`;
	/** Whether a year has pages: from the first page's to the last's, and not after this one. */
	const hasPages = (y: number) =>
		!!firstPage && !!lastPage && y >= Number(firstPage.slice(0, 4)) && y <= Number(lastPage.slice(0, 4)) && y <= Number(today.slice(0, 4));
	const monthOpen = (y: number, m: number) => hasPages(y) && ym(y, m) <= thisMonth;
	// A month or year is selected when the range picked touches it.
	const monthIn = (y: number, m: number) => !!drawn && drawn.a.slice(0, 7) <= ym(y, m) && ym(y, m) <= drawn.b.slice(0, 7);
	const yearIn = (y: number) => !!drawn && Number(drawn.a.slice(0, 4)) <= y && y <= Number(drawn.b.slice(0, 4));
	const title = $derived(
		mode === 'days' ? monthName.format(new Date(Date.UTC(view.y, view.m, 1))) : mode === 'months' ? String(view.y) : `${years[0]} – ${years[11]}`
	);
	const steps = { days: ['Previous month', 'Next month'], months: ['Previous year', 'Next year'], years: ['Earlier years', 'Later years'] } as const;

	/** The grid's focused cell gets the focus (after the grid is drawn). */
	async function focusCell() {
		await tick();
		grid?.querySelector<HTMLButtonElement>('[tabindex="0"]')?.focus();
	}

	function setMode(next: 'days' | 'months' | 'years') {
		mode = next;
		focusCell();
	}

	function pickYear(y: number) {
		if (!hasPages(y)) return;
		view = { y, m: view.m };
		setMode('months');
	}

	function pickMonth(y: number, m: number) {
		if (!monthOpen(y, m)) return;
		view = { y, m };
		// The days, with the focus on the same day of that month (or today).
		const dd = Number((focusDay || today).slice(8, 10));
		const want = `${ym(y, m)}-${String(Math.min(dd, new Date(Date.UTC(y, m + 1, 0)).getUTCDate())).padStart(2, '0')}`;
		focusDay = want > today ? today : want;
		setMode('days');
	}

	/** The arrows, Page Up and Page Down in the months and the years. */
	function gridMove(e: KeyboardEvent) {
		const by: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -3, ArrowDown: 3, PageUp: -12, PageDown: 12 };
		const n = by[e.key];
		if (n === undefined) return;
		e.preventDefault();
		if (mode === 'months') {
			const d = new Date(Date.UTC(view.y, view.m + n, 1));
			view = { y: d.getUTCFullYear(), m: d.getUTCMonth() };
		} else view = { y: view.y + n, m: view.m };
		focusCell();
	}

	function show(s: string) {
		const d = day(s);
		view = { y: d.getUTCFullYear(), m: d.getUTCMonth() };
	}

	async function focusCurrent() {
		await Promise.resolve();
		grid?.querySelector<HTMLButtonElement>(`[data-day="${focusDay}"]`)?.focus();
	}

	function setOpen(now: boolean) {
		if (now === open) return;
		open = now;
		if (now) {
			mode = 'days';
			focusDay = start ? end : today;
			show(focusDay);
			focusCurrent();
			return;
		}
		hover = undefined;
		if (pending) {
			// Closed after one pick: that one day.
			end = start;
			pending = false;
		}
		if (start !== from || end !== (to || from)) onchange?.(start, end);
	}

	function pick(s: string) {
		if (s > today) return;
		if (!pending) {
			start = s;
			end = s;
			pending = true;
		} else {
			if (s < start) [start, end] = [s, start];
			else end = s;
			pending = false;
		}
		hover = undefined;
		focusDay = s;
		if (!inView(s)) {
			show(s);
			focusCurrent();
		}
	}

	function move(s: string) {
		focusDay = s;
		if (pending && s <= today) hover = s;
		if (!inView(s)) show(s);
		focusCurrent();
	}

	function gridKey(e: KeyboardEvent) {
		const d = focusDay;
		const moves: Record<string, () => string> = {
			ArrowLeft: () => addDays(d, -1),
			ArrowRight: () => addDays(d, 1),
			ArrowUp: () => addDays(d, -7),
			ArrowDown: () => addDays(d, 7),
			Home: () => addDays(d, -weekday(d)),
			End: () => addDays(d, 6 - weekday(d)),
			PageUp: () => addMonths(d, e.shiftKey ? -12 : -1),
			PageDown: () => addMonths(d, e.shiftKey ? 12 : 1)
		};
		if (moves[e.key]) {
			e.preventDefault();
			move(moves[e.key]());
		}
	}

	/** The arrows beside the title: a month, a year or 12 years. */
	function step(n: number) {
		if (mode !== 'days') {
			view = { y: view.y + (mode === 'months' ? n : 12 * n), m: view.m };
			return;
		}
		const d = new Date(Date.UTC(view.y, view.m + n, 1));
		view = { y: d.getUTCFullYear(), m: d.getUTCMonth() };
	}

	function keydown(e: KeyboardEvent) {
		if (e.key !== 'Escape') return;
		if (open) {
			e.preventDefault();
			e.stopPropagation();
			setOpen(false);
			button.focus();
		} else tipOff = true;
	}

	function outside(e: PointerEvent) {
		if (open && !wrap.contains(e.target as Node)) setOpen(false);
	}
</script>

<svelte:document onpointerdown={outside} />

<!-- svelte-ignore a11y_no_static_element_interactions (Escape closes the calendar, and hides the tooltip: WCAG 1.4.13) -->
<div class="dates" bind:this={wrap} onkeydown={keydown}>
	<span class="has-tip" class:tip-off={tipOff || open} onpointerleave={() => (tipOff = false)} onfocusout={() => (tipOff = false)}>
	<button
		bind:this={button}
		type="button"
		class="secondary when"
		class:set={!!label}
		aria-label={label ? `Dates: ${label}` : 'Dates'}
		aria-haspopup="dialog"
		aria-expanded={open}
		aria-controls="{uid}-calendar"
		onclick={() => setOpen(!open)}
	>
		<Icon name="calendar" />{#if label}<span class="label">{label}</span>{/if}
	</button>
	<span class="tooltip" aria-hidden="true">Dates</span>
	</span>
	<input type="hidden" name="from" value={pending ? '' : start} />
	<input type="hidden" name="to" value={pending ? '' : end} />
	<div class="calendar" id="{uid}-calendar" role="dialog" aria-label="Choose dates" hidden={!open}>
		{#if open}
			<div class="head">
				<button type="button" class="ghost" aria-label={steps[mode][0]} onclick={() => step(-1)}><Icon name="previous" /></button>
				<h2 id="{uid}-month" aria-live="polite">
					{#if mode === 'years'}{title}{:else}<button
							type="button"
							class="ghost title"
							aria-describedby="{uid}-title-hint"
							onclick={() => setMode(mode === 'days' ? 'months' : 'years')}>{title}</button
						>{/if}
				</h2>
				<span id="{uid}-title-hint" hidden>{mode === 'days' ? 'Choose a month' : 'Choose a year'}</span>
				<button type="button" class="ghost" aria-label={steps[mode][1]} onclick={() => step(1)}><Icon name="next" /></button>
			</div>
			{#if mode === 'months'}
				<!-- svelte-ignore a11y_interactive_supports_focus (each month is a button; one of them is in the tab order) -->
				<div class="pick" role="grid" aria-labelledby="{uid}-month" tabindex="-1" bind:this={grid} onkeydown={gridMove}>
					{#each [0, 3, 6, 9] as row (row)}
						<div role="row">
							{#each [row, row + 1, row + 2] as m (m)}
								<button
									type="button"
									role="gridcell"
									class="cell"
									class:picked={monthIn(view.y, m)}
									class:now={ym(view.y, m) === thisMonth}
									data-month={m}
									aria-label={monthName.format(new Date(Date.UTC(view.y, m, 1)))}
									aria-selected={monthIn(view.y, m)}
									aria-disabled={monthOpen(view.y, m) ? undefined : 'true'}
									tabindex={m === view.m ? 0 : -1}
									onclick={() => pickMonth(view.y, m)}
									onfocus={() => (view = { y: view.y, m })}>{monthShort.format(new Date(Date.UTC(view.y, m, 1)))}</button
								>
							{/each}
						</div>
					{/each}
				</div>
			{:else if mode === 'years'}
				<!-- svelte-ignore a11y_interactive_supports_focus (each year is a button; one of them is in the tab order) -->
				<div class="pick" role="grid" aria-labelledby="{uid}-month" tabindex="-1" bind:this={grid} onkeydown={gridMove}>
					{#each [0, 3, 6, 9] as row (row)}
						<div role="row">
							{#each years.slice(row, row + 3) as y (y)}
								<button
									type="button"
									role="gridcell"
									class="cell"
									class:picked={yearIn(y)}
									class:now={y === Number(today.slice(0, 4))}
									data-year={y}
									aria-label={String(y)}
									aria-selected={yearIn(y)}
									aria-disabled={hasPages(y) ? undefined : 'true'}
									tabindex={y === view.y ? 0 : -1}
									onclick={() => pickYear(y)}
									onfocus={() => (view = { y, m: view.m })}>{y}</button
								>
							{/each}
						</div>
					{/each}
				</div>
			{:else}
			<div class="week" aria-hidden="true">
				{#each ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as d (d)}<span>{d}</span>{/each}
			</div>
			<!-- svelte-ignore a11y_interactive_supports_focus (each day is a button; one of them is in the tab order) -->
			<div
				class="days"
				class:previewing={pending && !!hover && hover !== start}
				role="grid"
				aria-labelledby="{uid}-month"
				tabindex="-1"
				bind:this={grid}
				onkeydown={gridKey}
				onpointerleave={() => pending && (hover = undefined)}
			>
				{#each weeks as week (week[0])}
					<div role="row">
						{#each week as d (d)}
							{@const inRange = !!drawn && d >= drawn.a && d <= drawn.b}
							<button
								type="button"
								role="gridcell"
								class="day"
								class:out={!inView(d)}
								class:today={d === today}
								class:in={inRange}
								class:first={inRange && d === drawn?.a}
								class:last={inRange && d === drawn?.b}
								class:picked={d === start || (!pending && d === end)}
								data-day={d}
								aria-label={long.format(day(d))}
								aria-selected={inRange}
								aria-disabled={d > today ? 'true' : undefined}
								tabindex={d === focusDay ? 0 : -1}
								onclick={() => pick(d)}
								onpointerenter={() => pending && d <= today && (hover = d)}
								onfocus={() => (focusDay = d)}
							>
								<span class="n">{Number(d.slice(8))}</span>
							</button>
						{/each}
					</div>
				{/each}
			</div>
			{/if}
			<div class="foot">
				<span aria-live="polite">{hint}</span>
				<button
					type="button"
					class="secondary"
					onclick={() => {
						setOpen(false);
						button.focus();
					}}>Done</button
				>
			</div>
		{/if}
	</div>
</div>

<style>
	.dates {
		position: relative;
		display: inline-flex;
	}
	.when .label {
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.when.set {
		border-color: var(--accent);
	}
	.calendar {
		position: absolute;
		top: 100%;
		left: 0;
		z-index: var(--layer-overlay);
		width: var(--calendar-width);
		max-width: var(--panel-max);
		margin-top: var(--space-2xs);
		padding: var(--space-md);
		background: var(--surface-2);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
		color: var(--fg);
		font-weight: var(--weight-normal);
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: var(--space-xs);
	}
	.head h2 {
		margin: 0;
		font-size: var(--text-base);
	}
	.head .title {
		min-height: 0;
		padding: var(--space-2xs) var(--space-sm);
		color: var(--fg);
		font-size: var(--text-base);
		font-weight: var(--weight-strong);
	}
	/* The months and the years: 3 a row. */
	.pick {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: var(--space-2xs);
	}
	.pick:focus {
		outline: none;
	}
	.pick [role='row'] {
		display: contents;
	}
	button.cell {
		min-width: 0;
		height: var(--calendar-day);
		min-height: 0;
		padding: 0;
		border: 0;
		border-radius: var(--radius-pill);
		background: transparent;
		color: var(--fg);
		font-weight: var(--weight-normal);
		font-variant-numeric: tabular-nums;
	}
	button.cell:hover {
		background: var(--hover);
		transform: none;
	}
	button.cell:active {
		transform: none;
	}
	button.cell.now {
		box-shadow: inset 0 0 0 var(--border-width) var(--control-border);
	}
	button.cell.picked,
	button.cell.picked:hover {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	button.cell[aria-disabled='true'],
	button.cell[aria-disabled='true']:hover {
		background: transparent;
		color: var(--muted);
		opacity: var(--disabled-opacity);
		cursor: default;
	}
	button.cell:focus-visible {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: calc(-1 * var(--focus-width));
	}
	.week,
	.days {
		display: grid;
		grid-template-columns: repeat(7, 1fr);
	}
	.week span {
		padding: var(--space-2xs) 0;
		color: var(--muted);
		font-size: var(--text-xxs);
		font-weight: var(--weight-strong);
		text-align: center;
	}
	.days {
		row-gap: var(--space-3xs);
	}
	.days:focus {
		outline: none;
	}
	.days [role='row'] {
		display: contents;
	}
	button.day {
		position: relative;
		width: 100%;
		min-width: 0;
		height: var(--calendar-day);
		min-height: 0;
		padding: 0;
		border: 0;
		border-radius: 0;
		background: transparent;
		color: var(--fg);
		font-weight: var(--weight-normal);
	}
	button.day:hover,
	button.day:active {
		background: transparent;
		transform: none;
	}
	/* The bar joining the range's days. */
	button.day::before {
		content: '';
		position: absolute;
		inset: var(--calendar-bar-inset) 0;
	}
	button.day.in::before {
		background: var(--accent-soft);
	}
	.previewing button.day.in::before {
		opacity: var(--preview-opacity);
	}
	button.day.first::before {
		left: var(--calendar-bar-inset);
		border-top-left-radius: var(--radius-pill);
		border-bottom-left-radius: var(--radius-pill);
	}
	button.day.last::before {
		right: var(--calendar-bar-inset);
		border-top-right-radius: var(--radius-pill);
		border-bottom-right-radius: var(--radius-pill);
	}
	button.day .n {
		position: relative;
		display: inline-grid;
		place-items: center;
		width: var(--calendar-mark);
		height: var(--calendar-mark);
		border-radius: var(--radius-round);
		font-variant-numeric: tabular-nums;
	}
	button.day:hover .n {
		background: var(--hover);
	}
	button.day.in:hover .n {
		background: transparent;
		box-shadow: inset 0 0 0 var(--border-width-strong) var(--accent);
	}
	button.day.today .n {
		box-shadow: inset 0 0 0 var(--border-width) var(--control-border);
	}
	button.day.picked .n,
	button.day.picked:hover .n {
		background: var(--accent);
		color: var(--accent-fg);
		font-weight: var(--weight-heavy);
		box-shadow: none;
	}
	button.day.out {
		color: var(--muted);
	}
	button.day[aria-disabled='true'] {
		color: var(--muted);
		opacity: var(--disabled-opacity);
		cursor: default;
	}
	button.day[aria-disabled='true']:hover .n {
		background: transparent;
		box-shadow: none;
	}
	button.day:focus-visible {
		outline: none;
	}
	button.day:focus-visible .n {
		outline: var(--focus-width) solid var(--focus);
		outline-offset: 0;
	}
	.foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
