<script lang="ts">
	// R35: located messages on a self-hosted map. Tiles come from the API's
	// PMTiles extract with range requests; fonts and icons are bundled. No
	// third party is contacted (ADR 0001). MapLibre is loaded only here.
	import 'maplibre-gl/dist/maplibre-gl.css';
	import * as maplibregl from 'maplibre-gl';
	import type { FeatureCollection, Point } from 'geojson';
	import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';
	import { Protocol } from 'pmtiles';
	import { layers, namedFlavor } from '@protomaps/basemaps';
	import { onMount } from 'svelte';
	import type { Message, Meta } from './api.ts';

	// variant 'single': the small map in a message's detail view (R35c), one
	// point and no clusters, below the text on phones (P2).
	let {
		messages,
		meta,
		focus = null,
		variant = 'full',
		onselect = () => {}
	}: {
		messages: Message[];
		meta: Meta;
		focus?: number | null;
		variant?: 'full' | 'single';
		onselect?: (ids: number[]) => void;
	} = $props();
	const single = $derived(variant === 'single');

	let container: HTMLDivElement;
	let map: maplibregl.Map | undefined;
	let ready = $state(false);
	// Without WebGL (turned off, or some headless browsers) there is no map;
	// the page still lists the located messages.
	let unavailable = $state(false);
	let fitted = false;

	maplibregl.setWorkerUrl(workerUrl);
	let protocolAdded = false;

	function token(name: string): string {
		return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
	}

	// A type-scale token in pixels, for text drawn by the map.
	function rem(name: string): number {
		return parseFloat(token(name)) * parseFloat(getComputedStyle(document.documentElement).fontSize);
	}

	function dark(): boolean {
		const t = document.documentElement.dataset.theme;
		return t ? t === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
	}

	function style(): maplibregl.StyleSpecification {
		const origin = location.origin;
		const flavor = dark() ? 'dark' : 'light';
		const base: maplibregl.StyleSpecification = {
			version: 8,
			glyphs: `${origin}/map/fonts/{fontstack}/{range}.pbf`,
			sprite: `${origin}/map/sprites/v4/${flavor}`,
			sources: {},
			layers: [{ id: 'background', type: 'background', paint: { 'background-color': token('--surface') } }]
		};
		if (meta.map_tiles) {
			base.sources.protomaps = {
				type: 'vector',
				url: `pmtiles://${origin}/api/v1/map/tiles.pmtiles`,
				// Without an override, the credit comes from the extract's metadata.
				...(meta.map_attribution ? { attribution: meta.map_attribution } : {})
			};
			base.layers = layers('protomaps', namedFlavor(flavor), { lang: 'en' }) as maplibregl.LayerSpecification[];
		}
		return base;
	}

	type Props = { id: number; priority: string; approximate: boolean };

	function geojson(): FeatureCollection<Point, Props> {
		return {
			type: 'FeatureCollection',
			features: messages
				.filter((m) => m.location)
				.map((m) => ({
					type: 'Feature',
					id: m.id,
					geometry: { type: 'Point', coordinates: [m.location!.lon, m.location!.lat] },
					properties: { id: m.id, priority: m.priority ?? 'none', approximate: m.location!.approximate }
				}))
		};
	}

	function addMessageLayers() {
		if (!map) return;
		map.addSource('messages', {
			type: 'geojson',
			data: geojson(),
			cluster: !single,
			clusterRadius: 36,
			clusterMaxZoom: 15
		});
		const fill = [
			'match',
			['get', 'priority'],
			'Emergency',
			token('--emergency-bg'),
			'Non-emergency',
			token('--nonemergency-bg'),
			'Admin',
			token('--admin-bg'),
			token('--accent')
		] as maplibregl.ExpressionSpecification;
		map.addLayer({
			id: 'clusters',
			type: 'circle',
			source: 'messages',
			filter: ['has', 'point_count'],
			paint: {
				'circle-color': token('--accent'),
				'circle-radius': ['step', ['get', 'point_count'], 14, 10, 18, 50, 24],
				'circle-stroke-width': 2,
				'circle-stroke-color': token('--bg')
			}
		});
		map.addLayer({
			id: 'cluster-count',
			type: 'symbol',
			source: 'messages',
			filter: ['has', 'point_count'],
			layout: { 'text-field': ['get', 'point_count_abbreviated'], 'text-font': ['Noto Sans Medium'], 'text-size': rem('--text-sm') },
			paint: { 'text-color': token('--bg') }
		});
		// An approximate location (the street's centroid) is drawn as a ring.
		map.addLayer({
			id: 'points',
			type: 'circle',
			source: 'messages',
			filter: ['!', ['has', 'point_count']],
			paint: {
				'circle-radius': 8,
				'circle-color': ['case', ['get', 'approximate'], token('--bg'), fill],
				'circle-stroke-color': fill,
				'circle-stroke-width': ['case', ['get', 'approximate'], 4, 2]
			}
		});
	}

	function fit(force = false) {
		if (!map || (fitted && !force)) return;
		const pts = messages.filter((m) => m.location);
		const target = focus != null ? pts.find((m) => m.id === focus) : undefined;
		if (target) {
			map.jumpTo({ center: [target.location!.lon, target.location!.lat], zoom: 16 });
			onselect([target.id]);
		} else if (pts.length) {
			const b = new maplibregl.LngLatBounds();
			for (const m of pts) b.extend([m.location!.lon, m.location!.lat]);
			map.fitBounds(b, { padding: 48, maxZoom: 15, duration: 0 });
		} else return;
		fitted = true;
	}

	onMount(() => {
		if (!protocolAdded) {
			maplibregl.addProtocol('pmtiles', new Protocol().tile);
			protocolAdded = true;
		}
		try {
			map = new maplibregl.Map({
			container,
			style: style(),
			center: [0, 0],
			zoom: 1,
			attributionControl: { compact: true, customAttribution: meta.attribution ? `Address data: ${meta.attribution}` : undefined },
			cooperativeGestures: false
			});
		} catch {
			unavailable = true;
			return;
		}
		map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-right');
		map.on('load', () => {
			addMessageLayers();
			ready = true;
			fit();
		});
		// WebKit sometimes keeps showing a frame from before the points were
		// drawn (seen in about 1 in 20 detail views in the A34 check); one
		// more frame once everything has loaded shows them.
		map.once('idle', () => map?.triggerRepaint());
		map.on('click', 'clusters', async (e) => {
			const f = e.features?.[0];
			if (!f || !map) return;
			const src = map.getSource('messages') as maplibregl.GeoJSONSource;
			const zoom = await src.getClusterExpansionZoom(f.properties.cluster_id);
			// Copies sent to several groups share one point and never separate:
			// list them instead of zooming forever.
			if (zoom > 15) {
				const leaves = await src.getClusterLeaves(f.properties.cluster_id, 100, 0);
				onselect(leaves.map((l) => l.properties!.id as number));
			} else {
				map.easeTo({ center: (f.geometry as Point).coordinates as [number, number], zoom });
			}
		});
		map.on('click', 'points', (e) => onselect((e.features ?? []).map((f) => f.properties.id as number)));
		for (const id of ['clusters', 'points']) {
			map.on('mouseenter', id, () => (map!.getCanvas().style.cursor = 'pointer'));
			map.on('mouseleave', id, () => (map!.getCanvas().style.cursor = ''));
		}
		// Follow the colour theme.
		const restyle = () => {
			if (!map) return;
			map.setStyle(style());
			map.once('style.load', addMessageLayers);
		};
		const mq = matchMedia('(prefers-color-scheme: dark)');
		mq.addEventListener('change', restyle);
		const mo = new MutationObserver(restyle);
		mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
		return () => {
			mq.removeEventListener('change', restyle);
			mo.disconnect();
			map?.remove();
			map = undefined;
		};
	});

	// New and reloaded messages.
	$effect(() => {
		const data = geojson();
		if (!ready || !map) return;
		(map.getSource('messages') as maplibregl.GeoJSONSource | undefined)?.setData(data);
		fit();
	});

	/** Re-fits the map, e.g. after the filters change. */
	export function refit() {
		fitted = false;
		fit(true);
	}
</script>

<div
	class="map"
	class:unavailable
	class:single
	bind:this={container}
	role="region"
	aria-label={single
		? 'Map showing where this message was placed. The location is also described in the details.'
		: 'Map of located messages. The same messages are listed below the map.'}
	data-testid="map"
	data-ready={unavailable ? 'unavailable' : ready}
>
	{#if unavailable}
		<p>
			This browser can't show the map (it needs WebGL, which may be turned off).
			{single ? 'The location is described in the details.' : 'The located messages are listed below.'}
		</p>
	{/if}
</div>

<style>
	.map {
		height: min(65vh, 40rem);
		min-height: 18rem;
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.single {
		height: 16rem;
		min-height: 0;
		border: 1px solid var(--border);
		border-radius: 0.3rem;
	}
	.unavailable {
		height: auto;
		min-height: 0;
		padding: 0.75rem;
	}
</style>
