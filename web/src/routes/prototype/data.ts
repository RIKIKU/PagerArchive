// Prototype only (ADR 0015): made-up pages on the example network (SPEC.md
// section 10), shaped as the API sends them, after redaction (P1). The
// templates are the fixture's "Example Fire" and a second, "Example Rescue";
// capcode 0999999 has no alias and 0000050 is the heartbeat capcode.
import type { Message, Priority } from '#lib/api.ts';

export const tz = 'Australia/Melbourne';

const fire = { agency: 'Example Fire Service', icon: '/prototype/example-fire.svg', colour: '#b3261e' };
const rescue = { agency: 'Example Rescue Unit', icon: '/prototype/example-rescue.svg', colour: '#0d3b66' };
const stations: Record<string, { id: number; name: string; t: typeof fire }> = {
	'0100001': { id: 1, name: 'Station 1', t: fire },
	'0100002': { id: 2, name: 'Station 2', t: fire },
	'0100003': { id: 3, name: 'Station 3', t: fire },
	'0100004': { id: 4, name: 'Station 4', t: fire },
	'0200001': { id: 5, name: 'Rescue 1', t: rescue }
};
const categories: Record<string, string> = { HSF: 'Structure fire', GRS: 'Grass fire', MED: 'Medical' };
const receivers = ['Hilltop receiver', 'Depot receiver'];

type Extra = Partial<Message>;

let next = 1;
function page(sentAt: string, capcode: string, priority: Priority | null, displayText: string, extra: Extra = {}): Message {
	const s = stations[capcode];
	const marker = priority === 'Emergency' ? '@@' : priority === 'Non-emergency' ? 'Hb' : priority === 'Admin' ? 'QD' : '';
	const sent = new Date(sentAt);
	return {
		id: next++,
		capcode,
		text: marker + displayText,
		display_text: displayText,
		priority,
		heartbeat: false,
		sent_at: sent.toISOString(),
		alias_id: s?.id ?? null,
		alias: s?.name ?? null,
		agency: s?.t.agency ?? null,
		icon: s?.t.icon ?? null,
		colour: s?.t.colour ?? null,
		ignored: false,
		category: extra.event_type ? (categories[extra.event_type.slice(0, 3)] ?? 'Other') : null,
		format: null,
		job_number: null,
		event_type: null,
		description: null,
		address: null,
		locality: null,
		grid_ref: null,
		group: null,
		units: null,
		extra: {},
		labels: {},
		location: null,
		receptions: receivers.map((client, i) => ({ client, received_at: new Date(sent.getTime() + 600 + i * 450).toISOString() })),
		...extra
	};
}

// Newest first, as the list shows them.
export const messages: Message[] = [
	page('2026-11-01T03:05:09Z', '0100001', 'Emergency', 'EX0000014 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1', {
		format: 'emergency',
		job_number: 'EX0000014',
		event_type: 'HSF1',
		description: 'SMOKE FROM ROOF',
		address: '12 FICTION ST',
		locality: 'MADEUPTON',
		grid_ref: '123456',
		units: ['T1', 'T2'],
		group: 'STN1',
		labels: { event_type: 'Structure fire, level 1' },
		location: { lon: -140.8657, lat: -34.8312, source: 'address', approximate: false }
	}),
	page('2026-11-01T02:58:41Z', '0000050', 'Admin', 'Network heartbeat 000123', { heartbeat: true }),
	page(
		'2026-11-01T02:41:17Z',
		'0100001',
		'Emergency',
		'EX0000013 MED1 PERSON COLLAPSED IN REAR CAR PARK OF COMMUNITY HALL, CALLER ON SCENE AND PERFORMING CPR AS DIRECTED, ENTRY VIA LANE OFF DEMO PL, AMBULANCE ALSO RESPONDING, FIRST CREW TO STAGE AT FRONT OF HALL AND MEET AMBULANCE @ 1 EXAMPLE ST, MADEUPTON GRID 123789 UNITS T1 R1 P2 #STN1',
		{
			format: 'emergency',
			job_number: 'EX0000013',
			event_type: 'MED1',
			description:
				'PERSON COLLAPSED IN REAR CAR PARK OF COMMUNITY HALL, CALLER ON SCENE AND PERFORMING CPR AS DIRECTED, ENTRY VIA LANE OFF DEMO PL, AMBULANCE ALSO RESPONDING, FIRST CREW TO STAGE AT FRONT OF HALL AND MEET AMBULANCE',
			address: '1 EXAMPLE ST',
			locality: 'MADEUPTON',
			grid_ref: '123789',
			units: ['T1', 'R1', 'P2'],
			group: 'STN1',
			location: { lon: -140.8673, lat: -34.8301, source: 'address', approximate: false }
		}
	),
	page('2026-11-01T02:12:03Z', '0100001', 'Non-emergency', 'RE EX0000012 CREWS RETURNING #STN1', {
		format: 'follow-up',
		job_number: 'EX0000012',
		group: 'STN1'
	}),
	page('2026-11-01T01:47:55Z', '0100002', 'Emergency', 'EX0000012 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2', {
		format: 'emergency',
		job_number: 'EX0000012',
		event_type: 'GRS3',
		description: 'GRASS FIRE',
		address: 'SAMPLE RD',
		locality: 'MADEUPTON',
		group: 'STN2',
		location: { lon: -140.8641, lat: -34.8288, source: 'street', approximate: true }
	}),
	page('2026-11-01T00:30:00Z', '0999999', 'Admin', 'PAGER TEST, PLEASE IGNORE'),
	page('2026-10-31T23:15:22Z', '0200001', 'Emergency', 'EX0000011 VEHICLE AND TRAILER ROLLED, ONE PERSON TRAPPED, FUEL LEAK @ SAMPLE RD NEAR DEMO PL, MADEUPTON', {
		job_number: 'EX0000011'
	}),
	page('2026-10-31T22:02:48Z', '0100003', 'Admin', 'Training Tuesday 7pm at the station'),
	page('2026-10-31T09:41:30Z', '0100004', 'Emergency', 'EX0000010 HSF2 SHED FIRE SPREADING TO FENCE @ 3 FICTION ST, TESTVILLE GRID 124401 UNITS T4 #STN4', {
		format: 'emergency',
		job_number: 'EX0000010',
		event_type: 'HSF2',
		description: 'SHED FIRE SPREADING TO FENCE',
		address: '3 FICTION ST',
		locality: 'TESTVILLE',
		grid_ref: '124401',
		units: ['T4'],
		group: 'STN4'
	}),
	page('2026-10-31T08:58:41Z', '0000050', 'Admin', 'Network heartbeat 000122', { heartbeat: true }),
	page('2026-10-31T07:20:11Z', '0200001', 'Non-emergency', 'RE EX0000009 ROAD OPEN, ALL UNITS RETURN TO STATION', {
		job_number: 'EX0000009'
	}),
	page('2026-10-31T05:33:09Z', '0100004', 'Admin', 'Hall unlocked from 6pm'),
	page('2026-10-31T04:10:56Z', '0100003', 'Non-emergency', 'RE EX0000008 STOP MESSAGE, FIRE OUT, ONE TANKER TO REMAIN #STN3', {
		format: 'follow-up',
		job_number: 'EX0000008',
		group: 'STN3'
	}),
	page('2026-10-31T03:02:27Z', '0100003', 'Emergency', 'EX0000008 GRS1 GRASS FIRE BESIDE RAIL LINE @ DEMO PL, MADEUPTON #STN3', {
		format: 'emergency',
		job_number: 'EX0000008',
		event_type: 'GRS1',
		description: 'GRASS FIRE BESIDE RAIL LINE',
		address: 'DEMO PL',
		locality: 'MADEUPTON',
		group: 'STN3',
		location: { lon: -140.8622, lat: -34.8335, source: 'street', approximate: true }
	}),
	page('2026-10-31T01:45:00Z', '0100002', 'Admin', 'Check the shed Saturday. Bring gloves.'),
	page('2026-10-30T22:19:14Z', '0100002', null, 'Plain text with no marker')
];
