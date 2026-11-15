// See https://svelte.dev/docs/kit/types#app.d.ts
declare global {
	namespace App {
		// R35f: a map open full screen as an overlay has a history entry of
		// its own (shallow routing), so Back closes it; the value names the map.
		interface PageState {
			mapFullscreen?: string;
		}
	}
}
export {};
