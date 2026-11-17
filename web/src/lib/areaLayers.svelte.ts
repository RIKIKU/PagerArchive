// R88c: which layers of areas this browser shows, one choice for every map
// (the map page, a message's detail view, a report's map), kept in
// localStorage under one key per layer ("shown" or "hidden"), and followed
// in the browser's other tabs (the storage event). Storage that can't be
// read or written leaves the choice working for this page's life. And each
// layer's outlines, fetched once per version for every map this page opens.

import type { FeatureCollection } from 'geojson';

const prefix = 'pagerarchive.map.areas.';

// Read again when a choice changes, here or in another tab.
let changed = $state(0);
// This page's own choices: kept whatever storage does.
const chosen = new Map<string, boolean>();

if (typeof window !== 'undefined') {
	window.addEventListener('storage', (e) => {
		// key null: the whole of storage was cleared.
		if (e.key === null) chosen.clear();
		else if (e.key.startsWith(prefix)) chosen.delete(e.key.slice(prefix.length));
		else return;
		changed++;
	});
}

/** Whether layer id is shown on this browser's maps (by default, yes). */
export function areaShown(id: string): boolean {
	void changed;
	const mine = chosen.get(id);
	if (mine !== undefined) return mine;
	try {
		return localStorage.getItem(prefix + id) !== 'hidden';
	} catch {
		return true; // No storage: shown.
	}
}

/** Shows or hides layer id on every map in this browser. */
export function showArea(id: string, shown: boolean) {
	chosen.set(id, shown);
	try {
		localStorage.setItem(prefix + id, shown ? 'shown' : 'hidden');
	} catch {
		// Not remembered: this page's maps follow it all the same.
	}
	changed++;
}

const outlines = new Map<string, Promise<FeatureCollection>>();

/** A layer's outlines at a version (R88c): fetched once, then the same for every map. */
export function areaOutlines(id: string, version: string): Promise<FeatureCollection> {
	const url = `/api/v1/map/areas/${encodeURIComponent(id)}?v=${encodeURIComponent(version)}`;
	let got = outlines.get(url);
	if (!got) {
		got = fetch(url).then((r) => {
			if (!r.ok) throw new Error(`${r.status}`);
			return r.json() as Promise<FeatureCollection>;
		});
		// A failure is tried again by the next map.
		got.catch(() => outlines.delete(url));
		outlines.set(url, got);
	}
	return got;
}
