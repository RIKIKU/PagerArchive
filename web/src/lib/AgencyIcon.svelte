<script lang="ts">
	// R66: a template's icon beside the alias name. It sits on a light tile,
	// so dark artwork stays visible in both themes, and its text alternative
	// is the agency name. The URL changes when the icon does, so the browser
	// fetches each icon once and keeps it.
	let { src, alt, large = false }: { src: string; alt: string; large?: boolean } = $props();
</script>

<span class="tile" class:large><img {src} {alt} width="64" height="64" decoding="async" /></span>

<style>
	.tile {
		display: inline-flex;
		flex: none;
		align-self: center;
		vertical-align: middle;
		padding: 0.1em;
		background: var(--icon-tile);
		border: 1px solid var(--border);
		border-radius: 0.25rem;
	}
	img {
		display: block;
		width: 1.25em;
		height: 1.25em;
	}
	.large img {
		width: 2.5rem;
		height: 2.5rem;
	}
</style>
