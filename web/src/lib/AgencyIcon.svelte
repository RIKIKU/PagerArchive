<script lang="ts">
	// R66: a template's icon beside the alias name. It sits on a light tile,
	// so dark artwork stays visible in both themes, and its text alternative
	// is the agency name. The URL changes when the icon does, so the browser
	// fetches each icon once and keeps it. In rows it's the size of the text
	// beside it, just before the alias; large on the alias and detail views.
	// Its drawn size is the tokens' (--tile-size), set before it loads, so it
	// needs no width or height of its own (R82c).
	let { src, alt, large = false }: { src: string; alt: string; large?: boolean } = $props();
</script>

<span class="tile" class:large><img {src} {alt} decoding="async" /></span>

<style>
	.tile {
		display: inline-flex;
		flex: none;
		align-self: center;
		vertical-align: middle;
		padding: var(--tile-pad);
		background: var(--icon-tile);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-small);
	}
	img {
		display: block;
		width: var(--tile-size);
		height: var(--tile-size);
	}
	.large img {
		width: var(--tile-size-large);
		height: var(--tile-size-large);
	}
</style>
