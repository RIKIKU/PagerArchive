<script lang="ts" module>
	/** One row of a bar chart: its label, its value and how it's written.
	 * previous: a thinner bar under it (the period before, R38b); range: the
	 * middle half from low to high, the median and the largest (R38f) instead
	 * of a bar. */
	export interface Bar {
		label: string;
		value: number;
		text: string;
		previous?: { value: number; text: string };
		range?: { low: number; high: number; median: number; max: number };
	}
</script>

<script lang="ts">
	// R38a: a simple horizontal bar chart of a report's numbers, drawn as an
	// SVG at the width it's given, with the tokens (R82c). Every value is
	// written beside its bar, so colour is never the only cue, and the table
	// beside the chart holds the same numbers. The chart is one image to
	// assistive technology, named by label; the table is what's read.
	import { px } from './chartImage.ts';

	let { bars, label, legend }: { bars: Bar[]; label: string; legend?: { now: string; previous: string } } = $props();

	let width = $state(0);
	const size = $derived.by(() => {
		void width;
		if (typeof document === 'undefined' || width === 0) return undefined;
		return { row: px('--chart-row'), bar: px('--chart-bar'), previous: px('--chart-bar-previous'), label: px('--chart-label'), value: px('--chart-value'), gap: px('--space-xs'), font: px('--text-sm') };
	});
	const layout = $derived.by(() => {
		if (!size) return undefined;
		const labelW = Math.min(width * 0.38, size.label);
		const valueW = Math.min(width * 0.3, size.value);
		const x0 = labelW + size.gap;
		const barW = Math.max(width - x0 - valueW - size.gap, 1);
		const max = Math.max(1, ...bars.map((b) => Math.max(b.value, b.previous?.value ?? 0, b.range?.max ?? 0)));
		const legendH = legend ? size.row * 2 : 0;
		const chars = Math.max(4, Math.floor(labelW / (size.font * 0.6)));
		return {
			labelW,
			x0,
			barW,
			legendH,
			height: legendH + bars.length * size.row,
			x: (v: number) => x0 + (barW * v) / max,
			fit: (s: string) => (s.length > chars ? `${s.slice(0, chars - 1)}…` : s)
		};
	});
</script>

<div class="wrap" bind:clientWidth={width} data-testid="chart">
	{#if size && layout && bars.length}
		<svg class="chart" width={width} height={layout.height} viewBox="0 0 {width} {layout.height}" role="img" aria-label={label}>
			{#if legend}
				<rect class="bar" x={layout.x0} y={size.row / 2 - size.bar / 2} width={size.bar} height={size.bar} />
				<text class="label" x={layout.x0 + size.bar + size.gap} y={size.row / 2} dominant-baseline="central">{legend.now}</text>
				<rect class="previous" x={layout.x0} y={size.row * 1.5 - size.previous / 2} width={size.bar} height={size.previous} />
				<text class="label" x={layout.x0 + size.bar + size.gap} y={size.row * 1.5} dominant-baseline="central">{legend.previous}</text>
			{/if}
			{#each bars as b, i (i)}
				{@const y = layout.legendH + i * size.row}
				{@const mid = y + size.row / 2}
				<text class="label" x={layout.labelW} y={mid} text-anchor="end" dominant-baseline="central">{layout.fit(b.label)}</text>
				<line class="axis" x1={layout.x0} x2={layout.x0} y1={y} y2={y + size.row} />
				{#if b.range}
					<line class="whisker" x1={layout.x(b.range.low)} x2={layout.x(b.range.max)} y1={mid} y2={mid} />
					<rect class="bar" x={layout.x(b.range.low)} y={mid - size.bar / 2} width={Math.max(layout.x(b.range.high) - layout.x(b.range.low), 2)} height={size.bar} />
					<line class="median" x1={layout.x(b.range.median)} x2={layout.x(b.range.median)} y1={mid - size.bar / 2 - 2} y2={mid + size.bar / 2 + 2} />
					<text class="value" x={layout.x(b.range.max) + size.gap} y={mid} dominant-baseline="central">{b.text}</text>
				{:else if b.previous}
					<rect class="bar" x={layout.x0} y={mid - size.bar / 2 - size.previous / 2} width={Math.max(layout.x(b.value) - layout.x0, b.value ? 2 : 0)} height={size.bar} />
					<rect class="previous" x={layout.x0} y={mid + size.bar / 2 - size.previous / 2 + 1} width={Math.max(layout.x(b.previous.value) - layout.x0, b.previous.value ? 2 : 0)} height={size.previous} />
					<text class="value" x={Math.max(layout.x(b.value), layout.x(b.previous.value)) + size.gap} y={mid} dominant-baseline="central">{b.text}</text>
				{:else}
					<rect class="bar" x={layout.x0} y={mid - size.bar / 2} width={Math.max(layout.x(b.value) - layout.x0, b.value ? 2 : 0)} height={size.bar} />
					<text class="value" x={layout.x(b.value) + size.gap} y={mid} dominant-baseline="central">{b.text}</text>
				{/if}
			{/each}
		</svg>
	{/if}
</div>

<style>
	.wrap {
		width: 100%;
		min-width: 0;
	}
	svg {
		display: block;
		overflow: visible;
	}
	.bar {
		fill: var(--chart-fill);
	}
	.previous {
		fill: var(--chart-previous);
	}
	.axis {
		stroke: var(--chart-grid);
		stroke-width: var(--border-width);
	}
	.whisker {
		stroke: var(--chart-fill);
		stroke-width: var(--border-width-strong);
	}
	.median {
		stroke: var(--chart-text);
		stroke-width: var(--border-width-strong);
	}
	.label,
	.value {
		fill: var(--chart-text);
		font-family: var(--font-ui);
		font-size: var(--text-sm);
	}
	.value {
		fill: var(--chart-muted);
	}
</style>
