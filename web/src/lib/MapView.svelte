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
	import { pushState } from '$app/navigation';
	import { page } from '$app/state';
	import type { Message, Meta } from './api.ts';
	import Icon from './Icon.svelte';
	import { icons } from './icons.ts';
	import { screen } from './screen.svelte.ts';

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
		strip = '',
		onselect = () => {}
	}: {
		messages?: Message[];
		meta: Meta;
		focus?: number | null;
		variant?: 'full' | 'single' | 'heat';
		heat?: HeatPoint[];
		// R35f: what the map is for, in a strip above it in full screen.
		strip?: string;
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

	// R35f: full screen. Where the browser can put an element full screen
	// (and not on a phone), the map and its strip are (the Fullscreen API);
	// otherwise, and on phones, they're a fixed overlay over the whole
	// viewport, moved to the end of the body so nothing on the page (a
	// stacking context, a container) holds it in, with a history entry of
	// its own so Back closes it. The map keeps its centre and zoom both ways.
	const uid = $props.id();
	let slot: HTMLDivElement;
	let frame: HTMLDivElement;
	let button: HTMLButtonElement | undefined = $state();
	let open = $state(false);
	let overlay = $state(false);
	let tipOff = $state(false);
	let inerted: Element[] = [];
	// Where the page was scrolled to, to come back to it.
	let scrolled = [0, 0];

	/** The map fills its new size: a frame after the change. */
	function resized() {
		requestAnimationFrame(() => map?.resize());
	}

	async function enter() {
		if (open) return;
		scrolled = [scrollX, scrollY];
		if (!screen.phone && document.fullscreenEnabled && typeof frame.requestFullscreen === 'function') {
			try {
				await frame.requestFullscreen();
				return;
			} catch {
				// Refused: the overlay instead.
			}
		}
		pushState('', { ...page.state, mapFullscreen: uid });
		showOverlay();
	}

	function showOverlay() {
		overlay = true;
		open = true;
		document.body.append(frame);
		// The page behind can't be reached (focus, screen readers) or scrolled.
		for (const el of document.body.children) {
			if (el !== frame && !el.hasAttribute('inert')) {
				el.setAttribute('inert', '');
				inerted.push(el);
			}
		}
		button?.focus({ preventScroll: true });
		resized();
	}

	function hideOverlay(refocus = true) {
		if (!overlay) return;
		overlay = false;
		open = false;
		for (const el of inerted) el.removeAttribute('inert');
		inerted = [];
		slot?.append(frame);
		if (refocus) button?.focus({ preventScroll: true });
		resized();
	}

	function exit() {
		if (document.fullscreenElement === frame) document.exitFullscreen();
		else if (overlay) {
			// Its history entry goes too, so Back then leaves the page as before.
			history.back();
		}
	}

	// The page behind doesn't scroll, or keep its scroll bar's room (app.css);
	// closed, it's where it was (the wider page may have scrolled less).
	$effect(() => {
		if (!open) return;
		document.documentElement.classList.add('map-full');
		return () => {
			document.documentElement.classList.remove('map-full');
			scrollTo(scrolled[0], scrolled[1]);
		};
	});

	// Back (the button or a phone's back gesture) closes the overlay: it
	// leaves the overlay's own history entry, a shallow one (SvelteKit's
	// pushState), so the page itself stays.
	$effect(() => {
		if (!overlay) return;
		const back = () => hideOverlay();
		addEventListener('popstate', back);
		return () => removeEventListener('popstate', back);
	});

	function frameKey(e: KeyboardEvent) {
		if (e.key !== 'Escape') return;
		tipOff = true;
		if (overlay) exit();
	}

	// The map's credits (R5, R35g): the address data's here; the background
	// map's and each shown layer of areas' come with their sources, so a
	// hidden layer's credit goes with it (R88c).
	function credits(): string[] | undefined {
		return meta.attribution ? [`Address data: ${meta.attribution}`] : undefined;
	}

	// R35g: the credits are MapLibre's compact attribution (it gathers each
	// shown source's credit), its ⓘ a real button here, "Map credits" (an
	// icon button like the map's others, tokens only), that opens and closes
	// the credit line beside it. They're open when the map opens and close
	// when the viewer moves, zooms or clicks the map, or after five seconds,
	// as the OpenStreetMap Foundation's attribution guidelines allow for an
	// interactive map; not while the pointer or focus is on them, and not
	// once the viewer has opened or closed them. Phones too: the guidelines
	// make no exception for small screens, so they don't start closed there.
	const creditsFor = 5_000;
	let creditsTimer: ReturnType<typeof setTimeout> | undefined;
	let creditsBox: HTMLDivElement | undefined;
	class Credits extends maplibregl.AttributionControl {
		onAdd(m: maplibregl.Map): HTMLElement {
			const details = super.onAdd(m);
			// The same control, as a box and a button instead of MapLibre's
			// details and summary (which aren't a button to assistive technology).
			const box = document.createElement('div');
			box.className = `${details.className} credits`;
			const line = this._innerContainer;
			line.id = `${uid}-credits`;
			const button = document.createElement('button');
			button.type = 'button';
			button.className = 'secondary icon-button credits-button';
			button.setAttribute('aria-label', 'Map credits');
			button.setAttribute('aria-controls', line.id);
			button.innerHTML = `<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${icons.help}</svg>`;
			button.addEventListener('click', () => {
				// The viewer's own choice, which no timer undoes.
				clearTimeout(creditsTimer);
				showCredits(!box.classList.contains('maplibregl-compact-show'));
			});
			box.append(line, button);
			details.remove();
			this._container = box;
			this._compactButton = button;
			creditsBox = box;
			// Its state, whoever changes it (MapLibre closes it on a drag too).
			const sync = () => button.setAttribute('aria-expanded', String(box.classList.contains('maplibregl-compact-show')));
			new MutationObserver(sync).observe(box, { attributes: true, attributeFilter: ['class'] });
			sync();
			return box;
		}
	}
	function showCredits(shown: boolean) {
		creditsBox?.classList.toggle('maplibregl-compact-show', shown);
	}
	function collapseCredits() {
		clearTimeout(creditsTimer);
		showCredits(false);
	}
	function collapseCreditsLater(after = creditsFor) {
		clearTimeout(creditsTimer);
		creditsTimer = setTimeout(() => {
			if (creditsBox?.matches(':hover, :focus-within')) collapseCreditsLater(1_000);
			else collapseCredits();
		}, after);
	}

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
	// Where the map is looking (centre and zoom), kept on its element too, so
	// a test can see full screen keeps it (R35f).
	let view = $state('');
	function looked() {
		if (!map) return;
		const c = map.getCenter();
		view = `${c.lng.toFixed(6)},${c.lat.toFixed(6)},${map.getZoom().toFixed(2)}`;
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
				'heatmap-color': ramp as maplibregl.ExpressionSpecification,
				'heatmap-opacity': 0.85
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

	// R88c: the network's layers of areas, each outlined and labelled under
	// the messages on the map page in its own style (tokens), the later over
	// the earlier, each shown or hidden by its own checkbox in the map's
	// layers panel, whose choice this browser remembers. The outlines are
	// asked for by their version, so the browser keeps them until the areas
	// change.
	const areaLayers = $derived(variant === 'full' ? (meta.areas ?? []) : []);
	const areasKey = (id: string) => `pagerarchive.map.areas.${id}`;
	function remembered(): Record<string, boolean> {
		const out: Record<string, boolean> = {};
		for (const l of meta.areas ?? []) {
			try {
				out[l.id] = localStorage.getItem(areasKey(l.id)) !== 'hidden';
			} catch {
				out[l.id] = true; // No storage: shown.
			}
		}
		return out;
	}
	let shownAreas: Record<string, boolean> = $state(remembered());
	// How many outlines of each layer are drawn, kept on its checkbox for a test.
	let areasDrawn: Record<string, number> = $state({});

	// A dash token as MapLibre's line-dasharray (in multiples of the line's
	// width), or undefined for a solid line.
	function dashes(name: string): number[] | undefined {
		const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
		return v && v !== 'none' ? v.split(/\s+/).map(Number) : undefined;
	}

	function addAreaLayers() {
		if (!map) return;
		for (const l of areaLayers) {
			const o = l.outline;
			const look = {
				line: token(`--map-area-${o}-line`),
				width: px(`--map-area-${o}-stroke`),
				dash: dashes(`--map-area-${o}-dash`),
				label: token(`--map-area-${o}-label`),
				font: font('--map-font'),
				size: rem('--text-sm'),
				halo: px('--map-stroke')
			};
			const visibility = shownAreas[l.id] === false ? 'none' : 'visible';
			const source = `areas-${l.id}`;
			// Its credit is the source's: MapLibre credits only the sources of shown layers.
			map.addSource(source, {
				type: 'geojson',
				data: `${location.origin}/api/v1/map/areas/${encodeURIComponent(l.id)}?v=${encodeURIComponent(l.version)}`,
				attribution: `${l.label} outlines: ${l.attribution}`
			});
			// Outlines are the features without a label point's mark.
			map.addLayer({
				id: `area-lines-${l.id}`,
				type: 'line',
				source,
				filter: ['!', ['has', 'label']],
				layout: { visibility, 'line-join': 'round', 'line-cap': o === 'dotted' ? 'round' : 'butt' },
				paint: { 'line-color': look.line, 'line-width': look.width, ...(look.dash ? { 'line-dasharray': look.dash } : {}) }
			});
			map.addLayer({
				id: `area-labels-${l.id}`,
				type: 'symbol',
				source,
				filter: ['has', 'label'],
				layout: {
					visibility,
					'text-field': ['concat', `${l.label} `, ['get', 'name']],
					'text-font': [look.font],
					'text-size': look.size,
					'text-allow-overlap': false
				},
				paint: { 'text-color': look.label, 'text-halo-color': token('--map-area-halo'), 'text-halo-width': look.halo }
			});
		}
	}

	function showAreas(id: string, shown: boolean) {
		shownAreas[id] = shown;
		try {
			localStorage.setItem(areasKey(id), shown ? 'shown' : 'hidden');
		} catch {
			// Not remembered.
		}
		for (const layer of [`area-lines-${id}`, `area-labels-${id}`]) if (map?.getLayer(layer)) map.setLayoutProperty(layer, 'visibility', shown ? 'visible' : 'none');
	}

	// The layers control (R88c): one small button in the map's corner that
	// opens a panel with a checkbox for each layer. Closed at first; Escape,
	// a click or focus elsewhere, or the button again closes it. Focus goes
	// to the first checkbox on opening, and back to the button on Escape.
	let layersBox: HTMLDivElement | undefined = $state();
	let layersButton: HTMLButtonElement | undefined = $state();
	let layersPanel: HTMLFieldSetElement | undefined = $state();
	let layersOpen = $state(false);
	let layersTipOff = $state(false);
	function openLayers(next: boolean) {
		layersOpen = next;
		if (next) requestAnimationFrame(() => layersPanel?.querySelector('input')?.focus());
	}
	function layersOutside(ev: Event) {
		if (layersOpen && layersBox && !layersBox.contains(ev.target as Node)) layersOpen = false;
	}
	function layersKey(ev: KeyboardEvent) {
		if (ev.key !== 'Escape') return;
		if (!layersOpen) {
			layersTipOff = true;
			return;
		}
		// Only the panel closes: not the full-screen overlay too.
		ev.stopPropagation();
		layersOpen = false;
		layersButton?.focus();
	}

	function addLayers() {
		if (variant === 'heat') addHeatLayers();
		else {
			addAreaLayers();
			addMessageLayers();
		}
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
			attributionControl: false,
			cooperativeGestures: false
			});
		} catch {
			unavailable = true;
			return;
		}
		map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-right');
		map.addControl(new Credits({ compact: true, customAttribution: credits() }), 'bottom-right');
		map.on('load', () => {
			addLayers();
			ready = true;
			fit();
			// R35g: the credits close after a while, once the map is drawn.
			map?.once('idle', () => {
				collapseCreditsLater();
			});
		});
		// WebKit sometimes keeps showing a frame from before the points were
		// drawn (seen in about 1 in 20 detail views in the A34 check); one
		// more frame once everything has loaded shows them.
		map.once('idle', () => map?.triggerRepaint());
		map.on('idle', () => {
			for (const l of areaLayers) {
				const id = `area-lines-${l.id}`;
				if (map?.getLayer(id)) areasDrawn[l.id] = shownAreas[l.id] === false ? 0 : map.queryRenderedFeatures({ layers: [id] }).length;
			}
		});
		// R35g: the viewer moving, zooming or clicking the map closes the
		// credits; the map's own moves (fitting the messages) don't.
		map.on('movestart', (e) => {
			if (e.originalEvent) collapseCredits();
		});
		map.on('click', collapseCredits);
		map.on('moveend', looked);
		map.on('resize', looked);
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
		// R35f: in and out of the browser's full screen (the button, or Esc).
		const fullscreen = () => {
			if (overlay) return;
			open = document.fullscreenElement === frame;
			resized();
		};
		document.addEventListener('fullscreenchange', fullscreen);
		return () => {
			document.removeEventListener('fullscreenchange', fullscreen);
			if (document.fullscreenElement === frame) document.exitFullscreen().catch(() => {});
			hideOverlay(false);
			frame.remove();
			mq.removeEventListener('change', restyle);
			mo.disconnect();
			clearTimeout(creditsTimer);
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

<svelte:window onclick={layersOutside} onfocusin={layersOutside} />

<!-- R35f: the slot keeps the map's room on the page while the overlay is away at the end of the body. -->
<div class="slot" class:away={overlay} class:small={single} bind:this={slot}>
	<!-- svelte-ignore a11y_no_static_element_interactions, a11y_no_noninteractive_element_interactions (Escape closes the overlay and hides the tooltip: WCAG 1.4.13) -->
	<div
		class="frame"
		class:open
		class:overlay
		bind:this={frame}
		role={open ? 'dialog' : undefined}
		aria-modal={open ? 'true' : undefined}
		aria-labelledby={open ? `${uid}-strip` : undefined}
		data-testid="map-frame"
		data-fullscreen={open ? (overlay ? 'overlay' : 'native') : undefined}
		onkeydown={frameKey}
	>
		{#if open}<p class="strip" id="{uid}-strip" data-testid="map-strip">{strip || 'Map'}</p>{/if}
		<div class="stage">
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
				data-view={view || undefined}
			>
				{#if unavailable}
					<p>
						This browser can't show the map (it needs WebGL, which may be turned off).
						{variant === 'heat' ? 'The localities are in the table.' : single ? 'The location is described in the details.' : 'The located messages are listed below.'}
					</p>
				{/if}
			</div>
			{#if ready}
				<span class="fs has-tip" class:tip-off={tipOff || open} onpointerleave={() => (tipOff = false)} onfocusout={() => (tipOff = false)}>
					<button
						bind:this={button}
						type="button"
						class="secondary"
						class:icon-button={!open}
						aria-label={open ? 'Exit full screen' : 'Full screen map'}
						onclick={() => (open ? exit() : enter())}
						><Icon name={open ? 'exitfullscreen' : 'fullscreen'} />{#if open}Exit full screen{/if}</button
					>
					<span class="tooltip" aria-hidden="true">Full screen map</span>
				</span>
				{#if areaLayers.length}
					<!-- svelte-ignore a11y_no_static_element_interactions (Escape closes the panel and hides the tooltip: WCAG 1.4.13) -->
					<div class="layers" bind:this={layersBox} onkeydown={layersKey}>
						<span
							class="layers-control has-tip"
							class:tip-off={layersTipOff || layersOpen}
							onpointerleave={() => (layersTipOff = false)}
							onfocusout={() => (layersTipOff = false)}
						>
							<button
								bind:this={layersButton}
								type="button"
								class="secondary icon-button"
								aria-label="Map layers"
								aria-expanded={layersOpen}
								aria-controls="{uid}-layers"
								data-testid="map-layers"
								onclick={() => openLayers(!layersOpen)}><Icon name="layers" /></button
							>
							<span class="tooltip" aria-hidden="true">Map layers</span>
						</span>
						<fieldset class="layers-panel" id="{uid}-layers" bind:this={layersPanel} hidden={!layersOpen} data-testid="map-layers-panel">
							<legend>Map layers</legend>
							{#each areaLayers as l (l.id)}
								<label class="layer"
									><input
										type="checkbox"
										checked={shownAreas[l.id] !== false}
										data-layer={l.id}
										data-drawn={areasDrawn[l.id] ?? 0}
										onchange={(e) => showAreas(l.id, e.currentTarget.checked)}
									/><span class="swatch" data-outline={l.outline} aria-hidden="true"></span>{l.label} outlines</label
								>
							{/each}
						</fieldset>
					</div>
				{/if}
			{/if}
		</div>
	</div>
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
	/* R35f: while the overlay is away, its room stays, so nothing behind it moves. */
	.slot.away {
		height: var(--map-height);
		min-height: var(--map-min-height);
	}
	.slot.away.small {
		height: var(--map-height-small);
		min-height: 0;
	}
	.stage {
		position: relative;
	}
	/* Full screen, either way: the strip, then the map filling the rest. */
	.frame.open {
		display: flex;
		flex-direction: column;
		background: var(--bg);
		color: var(--fg);
	}
	/* The overlay: the whole viewport as it is now (dynamic viewport height),
	   inside the safe areas, above the page's header and bottom bar. */
	.frame.overlay {
		position: fixed;
		inset: 0;
		z-index: var(--layer-overlay);
		height: var(--full-height);
		padding: env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left);
	}
	.strip {
		margin: 0;
		padding: var(--space-xs) var(--space-md);
		background: var(--surface);
		border-bottom: var(--border-width) solid var(--border);
		font-size: var(--text-sm);
		font-weight: var(--weight-strong);
		line-height: var(--leading-tight);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.open .stage {
		display: flex;
		flex: 1 1 auto;
		min-height: 0;
	}
	.open .map {
		flex: 1 1 auto;
		height: auto;
		min-height: 0;
		border: 0;
		border-radius: 0;
	}
	/* The button: the map's top-left corner (zoom is top right, the credit
	   bottom right), its tooltip opening to the right, inside the map. */
	.fs {
		position: absolute;
		top: var(--space-sm);
		left: var(--space-sm);
		z-index: var(--layer-content);
	}
	.fs > .tooltip {
		left: 0;
		transform: none;
	}
	/* R88c: the layers control, one icon button under the full-screen button:
	   in sight at the top of the map on a phone, where the bottom bar may
	   cover its foot. Its panel opens beside it, inside the map, as wide as
	   its labels need and no wider than the map leaves, scrolling if there
	   are more layers than fit: the rest of the map stays in sight. */
	.layers {
		display: contents;
	}
	.layers-control {
		position: absolute;
		left: var(--space-sm);
		top: calc(var(--space-sm) + var(--target) + var(--space-xs));
		z-index: var(--layer-content);
	}
	.layers-control > .tooltip {
		left: 0;
		transform: none;
	}
	.layers-panel {
		position: absolute;
		left: calc(var(--space-sm) + var(--target) + var(--space-xs));
		top: calc(var(--space-sm) + var(--target) + var(--space-xs));
		z-index: var(--layer-content);
		width: max-content;
		max-width: calc(100% - var(--target) - var(--space-xs) - 2 * var(--space-sm));
		max-height: calc(100% - var(--target) - var(--space-xs) - 2 * var(--space-sm));
		overflow-y: auto;
		margin: 0;
		padding: var(--space-xs) var(--space-sm);
		background: var(--surface-2);
		color: var(--fg);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
		box-shadow: var(--map-layers-shadow);
	}
	.layers-panel[hidden] {
		display: none;
	}
	.layers-panel legend {
		float: left;
		width: 100%;
		padding: var(--space-2xs) 0;
		font-size: var(--text-sm);
		font-weight: var(--weight-strong);
	}
	.layer {
		clear: both;
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		min-height: var(--target);
		font-size: var(--text-sm);
		cursor: pointer;
	}
	/* R35g: the credits, bottom right: the credit line (when open) and the
	   "Map credits" button beside it, in the app's look, not MapLibre's. */
	.map :global(.maplibregl-ctrl-attrib.credits) {
		display: flex;
		align-items: flex-end;
		gap: var(--space-xs);
		min-height: 0;
		margin: 0 var(--space-sm) var(--space-sm) 0;
		padding: 0;
		background: transparent;
		border-radius: 0;
		color: var(--fg);
		font: inherit;
	}
	.map :global(.credits .maplibregl-ctrl-attrib-inner) {
		max-width: var(--map-credits-width);
		padding: var(--space-2xs) var(--space-sm);
		background: var(--surface-2);
		color: var(--fg);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius-small);
		font-family: var(--font-ui);
		font-size: var(--text-xs);
		line-height: var(--leading-tight);
		box-shadow: var(--map-layers-shadow);
	}
	.map :global(.credits .maplibregl-ctrl-attrib-inner a) {
		color: var(--fg);
		text-decoration: underline;
	}
	.map :global(.credits .credits-button) {
		flex: none;
	}
	/* A sample of the layer's outline, the key to which line is which. */
	.swatch {
		display: inline-block;
		flex: none;
		width: var(--space-lg);
		border-top: var(--map-area-solid-stroke) solid var(--map-area-solid-line);
	}
	.swatch[data-outline='dashed'] {
		border-top: var(--map-area-dashed-stroke) dashed var(--map-area-dashed-line);
	}
	.swatch[data-outline='dotted'] {
		border-top: var(--map-area-dotted-stroke) dotted var(--map-area-dotted-line);
	}
</style>
