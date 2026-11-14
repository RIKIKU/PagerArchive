<script lang="ts" module>
	/** A locality on the heat layer (R38c): where, and its jobs. */
	export interface HeatPoint {
		name: string;
		lon: number;
		lat: number;
		jobs: number;
	}
</script>

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
	// point and no clusters, below the text on phones (P2). variant 'heat':
	// the busiest localities' report (R38c), a heat layer of localities
	// weighted by their jobs, each labelled with its number, instead of
	// messages.
	let {
		messages = [],
		meta,
		focus = null,
		variant = 'full',
		heat = [],
		onselect = () => {}
	}: {
		messages?: Message[];
		meta: Meta;
		focus?: number | null;
		variant?: 'full' | 'single' | 'heat';
		heat?: HeatPoint[];
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

	// A colour token as the browser draws it: tokens are written once for
	// both themes, light-dark(…), which only an element's computed colour
	// resolves (app.css).
	function token(name: string): string {
		const probe = document.createElement('span');
		probe.style.color = `var(${name})`;
		probe.hidden = true;
		document.documentElement.append(probe);
		const colour = getComputedStyle(probe).color;
		probe.remove();
		return colour;
	}

	// A type-scale token in pixels, for text drawn by the map.
	function rem(name: string): number {
		return parseFloat(getComputedStyle(document.documentElement).getPropertyValue(name)) * parseFloat(getComputedStyle(document.documentElement).fontSize);
	}

	// A size token in pixels (px, or rem times the root's font size).
	function px(name: string): number {
		const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
		return v.endsWith('rem') ? rem(name) : parseFloat(v);
	}

	// A font token, its quotes removed: the name of one of the map's own
	// fonts (static/map/fonts).
	function font(name: string): string {
		return getComputedStyle(document.documentElement).getPropertyValue(name).trim().replace(/^['"]|['"]$/g, '');
	}

	// Clusters grow at these numbers of messages (data, not look).
	const clusterSteps = [10, 50];

	// What the messages are drawn with, from the tokens (R82c), kept on the
	// map's element so a test can see the map follows them.
	let drawn = $state('');

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

	function heatGeojson(): FeatureCollection<Point, { name: string; jobs: number; label: string }> {
		return {
			type: 'FeatureCollection',
			features: heat.map((h) => ({
				type: 'Feature',
				geometry: { type: 'Point', coordinates: [h.lon, h.lat] },
				properties: { name: h.name, jobs: h.jobs, label: `${h.name}: ${h.jobs}` }
			}))
		};
	}

	// R38c: the heat layer, its colours and radius from the tokens, and a
	// label with each locality's name and jobs, so colour isn't the only cue.
	function addHeatLayers() {
		if (!map) return;
		const most = Math.max(1, ...heat.map((h) => h.jobs));
		const look = { radius: px('--map-heat-radius'), font: font('--map-font'), label: rem('--text-sm'), halo: px('--map-stroke') };
		drawn = JSON.stringify(look);
		// From none at no density to the accent at the most (data, then tokens).
		const ramp = ['interpolate', ['linear'], ['heatmap-density'], 0, token('--map-heat-none'), 0.2, token('--map-heat-low'), 1, token('--map-heat-high')];
		map.addSource('heat', { type: 'geojson', data: heatGeojson() });
		map.addLayer({
			id: 'heat',
			type: 'heatmap',
			source: 'heat',
			paint: {
				'heatmap-weight': ['/', ['get', 'jobs'], most],
				'heatmap-radius': look.radius,
				'heatmap-color': ramp as maplibregl.ExpressionSpecification
			}
		});
		map.addLayer({
			id: 'heat-labels',
			type: 'symbol',
			source: 'heat',
			layout: { 'text-field': ['get', 'label'], 'text-font': [look.font], 'text-size': look.label, 'text-allow-overlap': false },
			paint: { 'text-color': token('--fg'), 'text-halo-color': token('--bg'), 'text-halo-width': look.halo }
		});
	}

	function addLayers() {
		if (variant === 'heat') addHeatLayers();
		else addMessageLayers();
	}

	function addMessageLayers() {
		if (!map) return;
		const look = {
			point: px('--map-point-radius'),
			clusters: [px('--map-cluster-radius-small'), px('--map-cluster-radius-medium'), px('--map-cluster-radius-large')],
			distance: px('--map-cluster-distance'),
			stroke: px('--map-stroke'),
			ring: px('--map-ring-stroke'),
			font: font('--map-font'),
			label: rem('--text-sm'),
			colours: [token('--accent'), token('--bg'), token('--emergency-bg'), token('--nonemergency-bg'), token('--admin-bg')]
		};
		drawn = JSON.stringify(look);
		map.addSource('messages', {
			type: 'geojson',
			data: geojson(),
			cluster: !single,
			clusterRadius: look.distance,
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
				'circle-radius': ['step', ['get', 'point_count'], look.clusters[0], clusterSteps[0], look.clusters[1], clusterSteps[1], look.clusters[2]],
				'circle-stroke-width': look.stroke,
				'circle-stroke-color': token('--bg')
			}
		});
		map.addLayer({
			id: 'cluster-count',
			type: 'symbol',
			source: 'messages',
			filter: ['has', 'point_count'],
			layout: { 'text-field': ['get', 'point_count_abbreviated'], 'text-font': [look.font], 'text-size': look.label },
			paint: { 'text-color': token('--bg') }
		});
		// An approximate location (the street's centroid) is drawn as a ring.
		map.addLayer({
			id: 'points',
			type: 'circle',
			source: 'messages',
			filter: ['!', ['has', 'point_count']],
			paint: {
				'circle-radius': look.point,
				'circle-color': ['case', ['get', 'approximate'], token('--bg'), fill],
				'circle-stroke-color': fill,
				'circle-stroke-width': ['case', ['get', 'approximate'], look.ring, look.stroke]
			}
		});
	}

	function fit(force = false) {
		if (!map || (fitted && !force)) return;
		if (variant === 'heat') {
			if (!heat.length) return;
			const b = new maplibregl.LngLatBounds();
			for (const h of heat) b.extend([h.lon, h.lat]);
			map.fitBounds(b, { padding: 64, maxZoom: 13, duration: 0 });
			fitted = true;
			return;
		}
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
			addLayers();
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
		// Follow the colour theme and the look (R82d), when one of them has
		// changed: a restyle that changes nothing would only restart the map.
		const drawnIn = () => `${dark()} ${document.documentElement.dataset.look ?? ''}`;
		let styledIn = drawnIn();
		const restyle = () => {
			if (!map || drawnIn() === styledIn) return;
			styledIn = drawnIn();
			map.setStyle(style());
			map.once('style.load', addLayers);
		};
		const mq = matchMedia('(prefers-color-scheme: dark)');
		mq.addEventListener('change', restyle);
		const mo = new MutationObserver(restyle);
		mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-look'] });
		return () => {
			mq.removeEventListener('change', restyle);
			mo.disconnect();
			map?.remove();
			map = undefined;
		};
	});

	// New and reloaded messages, or localities.
	$effect(() => {
		const data = variant === 'heat' ? heatGeojson() : geojson();
		if (!ready || !map) return;
		(map.getSource(variant === 'heat' ? 'heat' : 'messages') as maplibregl.GeoJSONSource | undefined)?.setData(data);
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
	aria-label={variant === 'heat'
		? 'Map of jobs by locality. The same localities and their jobs are in the table.'
		: single
			? 'Map showing where this message was placed. The location is also described in the details.'
			: 'Map of located messages. The same messages are listed below the map.'}
	data-testid="map"
	data-ready={unavailable ? 'unavailable' : ready}
	data-drawn={drawn || undefined}
>
	{#if unavailable}
		<p>
			This browser can't show the map (it needs WebGL, which may be turned off).
			{variant === 'heat' ? 'The localities are in the table.' : single ? 'The location is described in the details.' : 'The located messages are listed below.'}
		</p>
	{/if}
</div>

<style>
	.map {
		height: var(--map-height);
		min-height: var(--map-min-height);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		overflow: hidden;
		background: var(--surface);
	}
	.single {
		height: var(--map-height-small);
		min-height: 0;
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-small);
	}
	.unavailable {
		height: auto;
		min-height: 0;
		padding: var(--space-md);
	}
</style>
