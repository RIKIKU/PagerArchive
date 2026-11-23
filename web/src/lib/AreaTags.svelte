<script lang="ts">
	// R88b: the areas a job's or a large event's pages are in (R87c), as
	// tags: each layer's label and the area's name, the layers in the
	// profile's order, each tag edged as the map outlines its layer (R88c).
	// None, and nothing drawn, when they're in no area or the profile has no
	// layers: never an "unknown".
	import { areaName, type AreaRef, type Meta } from './api.ts';

	let { meta, areas, label, testid }: { meta: Meta | undefined; areas: AreaRef[]; label: string; testid?: string } = $props();
</script>

{#if meta?.areas?.length && areas.length}
	<ul class="areas" aria-label={label} data-testid={testid}>
		{#each areas as a (a.layer + ' ' + a.name)}<li data-layer={a.layer} data-outline={meta.areas.find((l) => l.id === a.layer)?.outline}>{areaName(meta, a)}</li>{/each}
	</ul>
{/if}

<style>
	.areas {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs) var(--space-sm);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.areas li {
		padding: var(--space-2xs) var(--space-sm);
		border: var(--border-width) solid var(--area-tag-border);
		border-radius: var(--radius-pill);
		background: var(--area-tag-bg);
		color: var(--fg);
		font-size: var(--text-md);
		font-weight: var(--weight-strong);
	}
	/* Each layer's tags edged as the map outlines it (R88c). */
	.areas li[data-outline='dashed'] {
		border-style: dashed;
	}
	.areas li[data-outline='dotted'] {
		border-style: dotted;
	}
</style>
