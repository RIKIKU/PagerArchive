// R55: the number of alias requests waiting for review, shown on the Admin
// link. The header refreshes it; review screens refresh it after a change.
import { api } from '#lib/api.ts';

export const waiting = $state({ count: 0 });

export function refreshWaiting(): Promise<void> {
	return api
		.waitingRequests()
		.then((n) => {
			waiting.count = n;
		})
		.catch(() => {});
}
