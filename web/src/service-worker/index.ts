// The installed app's service worker (R90, ADR 0037). It keeps the app shell
// and nothing else: the build's own files (JavaScript and CSS, each named by
// its content), the icons, the manifest and the offline page, in one cache
// per build. Pages, API answers, searches, map tiles and anything about the
// person are never stored by it (P1): a request for anything that isn't a
// shell file isn't touched, and goes to the network as if there were no
// service worker. Navigations always go to the network, and when it can't be
// reached the offline page is shown. No push, no background sync, no
// notifications.
//
// A new release is a new service worker (its file lists and version change),
// which the browser finds when it checks this file on a navigation (the
// server sends it with no-cache). It takes over at once and deletes the old
// build's cache; the page itself always came from the network, so it's
// already the new release's.
import { version } from '$app/env';
import { assets, immutable } from '$app/manifest';

const sw = self as unknown as ServiceWorkerGlobalScope;

const CACHE = `shell-${version}`;
const OFFLINE = '/offline.html';

// Of web/static, only these are the shell: never the map's glyphs and
// sprites (map/), which the map fetches as it needs them.
const staticShell = /^\/(icons\/[a-z0-9-]+\.png|favicon(-32)?\.(svg|png)|apple-touch-icon\.png|manifest\.webmanifest|offline\.html)$/;
// Paths are relative to the app's root, which is the site's root here.
const rooted = (p: string) => '/' + p.replace(/^\//, '');
const SHELL = [
	...immutable.map((f) => rooted(f.path)).filter((p) => p.startsWith('/_app/immutable/')),
	...assets.map((f) => rooted(f.path)).filter((p) => staticShell.test(p))
];
const shell = new Set(SHELL);

sw.addEventListener('install', (event) => {
	event.waitUntil(precache().then(() => sw.skipWaiting()));
});

async function precache() {
	const cache = await caches.open(CACHE);
	await Promise.all(
		SHELL.map(async (path) => {
			// A file named by its content is the same in every build that has
			// it: an earlier build's copy saves downloading it again.
			const kept = path.startsWith('/_app/immutable/') ? await caches.match(path) : undefined;
			if (kept) return cache.put(path, kept);
			const res = await fetch(path, { cache: 'no-cache' });
			if (!res.ok) throw new Error(`${path}: ${res.status}`);
			return cache.put(path, res);
		})
	);
}

sw.addEventListener('activate', (event) => {
	event.waitUntil(
		(async () => {
			for (const name of await caches.keys()) if (name !== CACHE) await caches.delete(name);
			// No navigation preload: with it, a navigation the worker leaves to the
			// browser (/auth/callback) is requested twice, the preload and then the
			// browser's own, and the second finds the sign-in already used.
			await sw.registration.navigationPreload?.disable();
			await sw.clients.claim();
		})()
	);
});

sw.addEventListener('fetch', (event) => {
	const req = event.request;
	if (req.method !== 'GET') return;
	const url = new URL(req.url);
	if (url.origin !== sw.location.origin) return;
	if (req.mode === 'navigate') {
		// Signing in and out redirect to Authentik and back: the browser
		// follows those itself.
		if (url.pathname.startsWith('/auth/') || url.pathname.startsWith('/api/')) return;
		event.respondWith(navigate(req));
		return;
	}
	if (shell.has(url.pathname) && !url.search) event.respondWith(fromShell(req, url.pathname));
});

/** A page, from the network; the offline page if it can't be reached. Never stored. */
async function navigate(req: Request): Promise<Response> {
	try {
		return await fetch(req);
	} catch {
		return (await caches.match(OFFLINE, { cacheName: CACHE })) ?? Response.error();
	}
}

/** A shell file, from this build's cache; from the network (not stored) if it isn't there. */
async function fromShell(req: Request, path: string): Promise<Response> {
	const cached = await caches.match(path, { cacheName: CACHE });
	return cached ?? fetch(req);
}
