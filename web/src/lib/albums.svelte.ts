// R77, R82: the signed-in user's albums, for the side menu's Albums group (one
// entry each, with its count) and "Add to album". The frame loads them; every
// change made here refreshes them. Names only, from the API: nothing is kept
// in the browser's storage.
import { api, type Album } from './api.ts';

export const albums = $state({ list: [] as Album[], loaded: false });

export function refreshAlbums(): Promise<void> {
	return api
		.albums()
		.then((l) => {
			albums.list = l;
			albums.loaded = true;
		})
		.catch(() => {});
}

/** What R82a says, in one line, on the album screens. */
export const retentionLine =
	'Albums keep messages only as long as the archive does: retention deletes them here too, and an album it empties goes, unless it has a description.';
