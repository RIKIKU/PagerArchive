// Screenshots of the example monitoring stack for docs/monitoring.md, taken
// with Playwright's Chromium from web/'s dependencies. Run by
// scripts/monitoring-screenshots.sh.
//
// usage: GRAFANA_URL=... GRAFANA_PASSWORD=... node scripts/monitoring-screenshots.cjs DIR
// TRACE_WAIT_MS (default 90000) is how long the trace view may take to draw.
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(require.resolve('playwright', { paths: [path.join(__dirname, '..', 'web')] }));

const dir = process.argv[2];
const grafana = process.env.GRAFANA_URL;
const traceWait = Number(process.env.TRACE_WAIT_MS || 90000);
const auth = 'Basic ' + Buffer.from('admin:' + process.env.GRAFANA_PASSWORD).toString('base64');

// The spans in a trace from Tempo's API (OTLP JSON).
function spans(t) {
	const out = [];
	for (const b of t.batches || t.resourceSpans || []) {
		for (const s of b.scopeSpans || b.instrumentationLibrarySpans || []) out.push(...(s.spans || []));
	}
	return out;
}

// The distinct span names in a trace.
function spanNames(t) {
	return [...new Set(spans(t).map((sp) => sp.name))];
}

// How long the client's buffer span lasted, in ms (0 if there's none).
function bufferMs(t) {
	const b = spans(t).find((sp) => sp.name === 'buffer');
	return b ? Number((BigInt(b.endTimeUnixNano) - BigInt(b.startTimeUnixNano)) / 1000000n) : 0;
}

async function api(p) {
	const res = await fetch(grafana + p, { headers: { Authorization: auth } });
	if (!res.ok) throw new Error(`${p}: ${res.status}`);
	return res.json();
}

(async () => {
	// The newest stored page's trace from the screenshots' receiver
	// (monitoring-screenshots.sh): the API's ingest span, found in Tempo.
	const q = encodeURIComponent('{resource.service.name="pagerarchive" && name="ingest" && span.pagerarchive.ingest.outcome="stored" && span.pagerarchive.client.id="hill-receiver"}');
	const end = Math.floor(Date.now() / 1000);
	const found = await api(`/api/datasources/proxy/uid/tempo/api/search?q=${q}&limit=50&start=${end - 3600}&end=${end}`);
	const traces = (found.traces || []).sort((a, b) => Number(b.startTimeUnixNano) - Number(a.startTimeUnixNano));
	if (!traces.length) throw new Error('no page traces in Tempo');
	// The page held while the API was down, if it's there: its buffer span
	// (not the whole trace, which a failing integration's retries lengthen)
	// lasted over 10 s.
	let trace = traces[0].traceID;
	let names = spanNames(await api(`/api/datasources/proxy/uid/tempo/api/traces/${trace}`));
	for (const t of traces) {
		const full = await api(`/api/datasources/proxy/uid/tempo/api/traces/${t.traceID}`);
		if (bufferMs(full) > 10000) {
			trace = t.traceID;
			names = spanNames(full);
			console.log(`trace ${trace}: the client buffered the page for ${(bufferMs(full) / 1000).toFixed(1)} s`);
			break;
		}
	}
	// The trace must hold the whole path: the client's steps, the API's and
	// at least one query (sqlc names: InsertMessage, FindDuplicate ...).
	const lacking = ['page', 'decode', 'send', 'ingest', 'ingest.redact', 'ingest.parse', 'ingest.store'].filter((n) => !names.includes(n));
	if (!names.some((n) => /^[A-Z][A-Za-z0-9]+$/.test(n))) lacking.push('a database query');
	if (lacking.length) throw new Error(`trace ${trace} has no ${lacking.join(', ')}: its spans are ${names.join(', ')}`);
	console.log(`trace ${trace}: ${names.length} spans: ${names.join(', ')}`);

	const browser = await chromium.launch();
	const page = await browser.newPage({ viewport: { width: 1600, height: 2000 }, extraHTTPHeaders: { Authorization: auth } });
	const shot = async (name, url, wait) => {
		await page.goto(grafana + url);
		await page.waitForLoadState('networkidle');
		await page.waitForTimeout(wait || 4000);
		await page.screenshot({ path: path.join(dir, name), fullPage: true });
		console.log('screenshot', name);
	};
	// The trace view must have drawn every one of the trace's spans before
	// the screenshot: no picture of an empty or loading page. Each span's
	// row in Grafana 13's trace view names it in an "endpointName" span.
	const pane = { datasource: 'tempo', queries: [{ refId: 'A', datasource: { type: 'tempo', uid: 'tempo' }, queryType: 'traceql', query: trace }], range: { from: 'now-1h', to: 'now' } };
	fs.rmSync(path.join(dir, 'trace.png'), { force: true });
	const opened = Date.now();
	await page.goto(grafana + `/explore?schemaVersion=1&orgId=1&panes=${encodeURIComponent(JSON.stringify({ a: pane }))}`);
	const rows = { sel: '[data-testid="TimelineRowCell"] [class*="endpointName"]', want: names };
	const notDrawn = ({ sel, want }) => {
		const shown = new Set([...document.querySelectorAll(sel)].map((e) => e.textContent.trim()));
		return want.filter((n) => !shown.has(n));
	};
	try {
		await page.waitForFunction(({ sel, want }) => {
			const shown = new Set([...document.querySelectorAll(sel)].map((e) => e.textContent.trim()));
			return want.every((n) => shown.has(n));
		}, rows, { timeout: traceWait, polling: 500 });
	} catch (e) {
		const missing = await page.evaluate(notDrawn, rows);
		throw new Error(`the trace view didn't draw ${missing.join(', ')} within ${traceWait / 1000} s; no screenshot taken`);
	}
	console.log(`the trace view drew all ${names.length} span names ${((Date.now() - opened) / 1000).toFixed(1)} s after opening`);
	await page.waitForTimeout(1000);
	await page.screenshot({ path: path.join(dir, 'trace.png'), fullPage: true });
	console.log('screenshot trace.png');
	await shot('dashboard-page-path.png', '/d/pa-page-path?orgId=1&from=now-30m&to=now');
	await shot('dashboard-requests.png', '/d/pa-requests?orgId=1&from=now-30m&to=now');
	await shot('dashboard-clients.png', '/d/pa-clients?orgId=1&from=now-30m&to=now');
	await shot('alert-firing.png', '/alerting/grafana/pa-client-not-receiving/view?tab=instances', 5000);
	await browser.close();
})().catch((e) => {
	console.error(e);
	process.exit(1);
});
