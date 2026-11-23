// R77c: the side menu's new-large-event dot. The server decides, per user:
// a large event started within the last 4 hours and opened after they last
// opened the Large events page. Its stream says so at once and again
// whenever an event opens or the user opens that page anywhere (another
// tab, browser or device), and the dot goes by itself 4 hours after the
// newest new event's start, timed here from the server's own clock.
import { api, type NewLargeEvent } from './api.ts';

export const largeEventDot = $state({ on: false });

let timer: ReturnType<typeof setTimeout> | undefined;

function apply(s: NewLargeEvent) {
	largeEventDot.on = s.new;
	clearTimeout(timer);
	timer = undefined;
	if (s.new && s.until) {
		// Timed by the server's clock: until - now, whatever this one says.
		const left = Date.parse(s.until) - Date.parse(s.now);
		timer = setTimeout(() => {
			largeEventDot.on = false;
			// Ask again, in case another event is new by then.
			api.newLargeEvent().then(apply, () => {});
		}, Math.max(0, left));
	}
}

/** Follows the signed-in user's dot; returns the function that stops it. */
export function followLargeEventDot(): () => void {
	let es: EventSource | undefined;
	let stopped = false;
	const open = () => {
		es = new EventSource('/api/v1/me/new-large-event/stream');
		es.addEventListener('state', (e) => {
			try {
				apply(JSON.parse((e as MessageEvent<string>).data));
			} catch {
				// a garbled event: wait for the next
			}
		});
		// The user's status or role changed: open it again, as the message
		// list's stream does (ADR 0010 §4).
		es.addEventListener('reconnect', () => {
			es?.close();
			if (!stopped) setTimeout(open, 1000);
		});
	};
	open();
	return () => {
		stopped = true;
		es?.close();
		clearTimeout(timer);
	};
}

/** The user opened the Large events page: the dot goes, here and (by the server) in their other tabs. */
export function seeLargeEvents(): Promise<void> {
	largeEventDot.on = false;
	return api.largeEventsSeen().then(apply, () => {});
}
