// Screenshots of the example monitoring stack for docs/monitoring.md, taken
// with Playwright's Chromium from web/'s dependencies. Run by
// scripts/monitoring-screenshots.sh.
//
// usage: GRAFANA_URL=... GRAFANA_PASSWORD=... node scripts/monitoring-screenshots.cjs DIR
const path = require('node:path');
const { chromium } = require(require.resolve('playwright', { paths: [path.join(__dirname, '..', 'web')] }));

const dir = process.argv[2];
const grafana = process.env.GRAFANA_URL;
const auth = 'Basic ' + Buffer.from('admin:' + process.env.GRAFANA_PASSWORD).toString('base64');

async function api(p) {
	const res = await fetch(grafana + p, { headers: { Authorization: auth } });
	if (!res.ok) throw new Error(`${p}: ${res.status}`);
	return res.json();
}

(async () => {
	// The newest stored page's trace: the API's ingest span, found in Tempo.
	const q = encodeURIComponent('{resource.service.name="pagerarchive" && name="ingest" && span.pagerarchive.ingest.outcome="stored"}');
	const end = Math.floor(Date.now() / 1000);
	const found = await api(`/api/datasources/proxy/uid/tempo/api/search?q=${q}&limit=50&start=${end - 3600}&end=${end}`);
	const traces = (found.traces || []).sort((a, b) => Number(b.startTimeUnixNano) - Number(a.startTimeUnixNano));
	if (!traces.length) throw new Error('no page traces in Tempo');
	// The page held while the API was down, if it's there: its trace shows the buffering.
	let trace = traces[0].traceID;
	for (const t of traces) {
		const full = await api(`/api/datasources/proxy/uid/tempo/api/traces/${t.traceID}`);
		if (JSON.stringify(full).includes('"buffer"') && Number(t.durationMs) > 10000) { trace = t.traceID; break; }
	}

	const browser = await chromium.launch();
	const page = await browser.newPage({ viewport: { width: 1600, height: 2000 }, extraHTTPHeaders: { Authorization: auth } });
	const shot = async (name, url, wait) => {
		await page.goto(grafana + url);
		await page.waitForLoadState('networkidle');
		await page.waitForTimeout(wait || 4000);
		await page.screenshot({ path: path.join(dir, name), fullPage: true });
		console.log('screenshot', name);
	};
	const pane = { datasource: 'tempo', queries: [{ refId: 'A', datasource: { type: 'tempo', uid: 'tempo' }, queryType: 'traceql', query: trace }], range: { from: 'now-1h', to: 'now' } };
	await shot('trace.png', `/explore?schemaVersion=1&orgId=1&panes=${encodeURIComponent(JSON.stringify({ a: pane }))}`, 6000);
	await shot('dashboard-page-path.png', '/d/pa-page-path?orgId=1&from=now-30m&to=now');
	await shot('dashboard-requests.png', '/d/pa-requests?orgId=1&from=now-30m&to=now');
	await shot('dashboard-clients.png', '/d/pa-clients?orgId=1&from=now-30m&to=now');
	await shot('alert-firing.png', '/alerting/grafana/pa-client-not-receiving/view?tab=instances', 5000);
	await browser.close();
})().catch((e) => {
	console.error(e);
	process.exit(1);
});
