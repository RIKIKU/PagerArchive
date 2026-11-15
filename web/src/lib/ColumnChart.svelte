<script lang="ts" module>
	/** One column: its label on the axis, its value and how it's written. */
	export interface Column {
		label: string;
		value: number;
		text: string;
	}
</script>

<script lang="ts">
	// R38a: a simple column chart over time (R38i), drawn as an SVG at the
	// width it's given, with the tokens (R82c). Columns with a value have it
	// written above them where there's room; the table beside the chart
	// holds every number. Labels along the axis are thinned to fit.
	import { px } from './chartImage.ts';

	let { columns, label }: { columns: Column[]; label: string } = $props();

	let width = $state(0);
	const layout = $derived.by(() => {
		void width;
		if (typeof document === 'undefined' || width === 0 || columns.length === 0) return undefined;
		const font = px('--text-sm');
		const height = px('--chart-height');
		const gap = px('--space-xs');
		const axis = font * 2;
		const top = font * 1.5;
		const plot = height - axis - top;
		const step = width / columns.length;
		const max = Math.max(1, ...columns.map((c) => c.value));
		const longest = Math.max(...columns.map((c) => c.label.length));
		const every = Math.max(1, Math.ceil((longest * font * 0.6 + gap) / step));
		const valueFits = step >= Math.max(...columns.map((c) => c.text.length)) * font * 0.6;
		return { font, height, axis, top, plot, step, max, every, valueFits, bar: Math.max(step - Math.min(gap, step / 4), 1) };
	});
</script>

<div class="wrap" bind:clientWidth={width} data-testid="chart">
	{#if layout}
		{@const base = layout.top + layout.plot}
		<svg class="chart" width={width} height={layout.height} viewBox="0 0 {width} {layout.height}" role="img" aria-label={label}>
			<line class="axis" x1="0" x2={width} y1={base} y2={base} />
			{#each columns as c, i (i)}
				{@const h = (layout.plot * c.value) / layout.max}
				{@const x = i * layout.step}
				{#if c.value > 0}
					<rect class="bar" x={x + (layout.step - layout.bar) / 2} y={base - Math.max(h, 2)} width={layout.bar} height={Math.max(h, 2)} />
					{#if layout.valueFits}
						<text class="value" x={x + layout.step / 2} y={base - Math.max(h, 2) - layout.font * 0.4} text-anchor="middle">{c.text}</text>
					{/if}
				{/if}
				{#if i % layout.every === 0}
					<text class="label" x={x + (layout.every === 1 ? layout.step / 2 : 0)} y={base + layout.font * 1.3} text-anchor={layout.every === 1 ? 'middle' : 'start'}>{c.label}</text>
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
	.axis {
		stroke: var(--chart-grid);
		stroke-width: var(--border-width);
	}
	.label,
	.value {
		fill: var(--chart-muted);
		font-family: var(--font-ui);
		font-size: var(--text-sm);
	}
	.value {
		fill: var(--chart-text);
	}
</style>
