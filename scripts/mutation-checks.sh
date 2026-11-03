#!/bin/sh
# Mutation proof for the security checks (M8, M9, M10), the print sheet
# (M11), telemetry (M12), search (M13a), the import (M16) and installing and
# upgrading (M17): break one check
# at a time in a throwaway copy of the repository, run the test that guards it, and show
# the test failing. Every mutation must fail its test; the unmutated copy
# must pass.
#
# usage: scripts/mutation-checks.sh
# needs: Go and PAGERARCHIVE_TEST_DATABASE_URL (as for go test), with
# PAGERARCHIVE_A75_CONTAINER naming that database's container, and sudo with no
# password, for A75's cold starts (they drop the host's page cache); for the
# print sheet's browser checks, what scripts/e2e.sh needs (Docker, Node with
# web/ dependencies installed, Playwright's Chromium), and no other e2e.sh
# running; for the module's checks pwsh (PA_PWSH, default: pwsh on PATH)
# with Pester 5.9.1 on PSModulePath; for the alert rule's, what
# scripts/monitoring-checks.sh needs. Linux only: the module's checks are
# the off-Windows ones.
#
# PA_MUT_ONLY, if set, runs only the mutations whose name matches it (grep -E),
# and then none of the unmutated runs at the end.
set -u
src=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd "$src" && tar -cf - --exclude=.git --exclude=web/node_modules --exclude=web/test-results --exclude=web/playwright-report .) | (cd "$work" && tar -xf -)
cd "$work" || exit 1
PA_PWSH=${PA_PWSH:-$(command -v pwsh || true)}
survived=0

# mutate <name> <file> <test> <old> <new>: replace exact text, run the test,
# and restore. <test> is "go:<regexp>" (the acceptance checks),
# "telemetry:<regexp>" (internal/telemetry's tests), "unit:<package
# dir>:<regexp>" (another package's tests), "pester:<file>", "e2e:<grep>"
# "mon:<regexp>" (the example stack's checks: TestA65, TestA67),
# "a75:<regexp>" (an acceptance check with A75's 4,562,500 pages),
# "a76:<regexp>" (A76's checks through the command, scripts/a76-checks.sh) or
# "stack:<regexp>" (the account checks against Authentik, through
# scripts/stack-checks.sh with A23's first admin) or "a77:" (the upgrade,
# through scripts/a77.sh).
#
# mutate <name> <file> <test> <old> <new> <file2> <old2> <new2>: two
# replacements, in two files, as one mutation (M12).
#
# With must set, the test must fail with that text in its output, not just
# fail (ADR 0014: a broken lock must fail A33 as a security failure).
must=""
mutate() {
	name=$1 file=$2 test=$3 old=$4 new=$5 file2=${6:-} old2=${7:-} new2=${8:-}
	if [ -n "${PA_MUT_ONLY:-}" ] && ! printf '%s\n' "$name" | grep -qE "$PA_MUT_ONLY"; then
		return
	fi
	cp "$file" "$work/.orig"
	[ -z "$file2" ] || cp "$file2" "$work/.orig2"
	OLD=$old NEW=$new perl -0pi -e '$n = s/\Q$ENV{OLD}\E/$ENV{NEW}/g; END { exit($n ? 0 : 1) }' "$file" &&
		{ [ -z "$file2" ] || OLD=$old2 NEW=$new2 perl -0pi -e '$n = s/\Q$ENV{OLD}\E/$ENV{NEW}/g; END { exit($n ? 0 : 1) }' "$file2"; } || {
		echo "=== NOT APPLIED: $name"
		survived=1
		cp "$work/.orig" "$file"
		[ -z "$file2" ] || cp "$work/.orig2" "$file2"
		return
	}
	echo "=== mutation: $name ($file${file2:+, $file2})"
	diff "$work/.orig" "$file" | sed 's/^/    /'
	[ -z "$file2" ] || diff "$work/.orig2" "$file2" | sed 's/^/    /'
	case $test in
	go:*)
		out=$(go test -p 1 -count=1 -timeout 10m -v -run "${test#go:}" ./internal/acceptance/ 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: ' | grep -vE ': 401 invalid_token$|^\s+[a-z0-9_]+_test.go:[0-9]+: (A53: user B|ADR 0010)' | head -4
		;;
	telemetry:*)
		out=$(go test -count=1 -v -run "${test#telemetry:}" ./internal/telemetry/ 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: ' | head -4
		;;
	unit:*)
		spec=${test#unit:}
		out=$(go test -count=1 -v -run "${spec#*:}" "./${spec%%:*}/" 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: ' | head -4
		;;
	mon:*)
		out=$(PA_MON_RUN="${test#mon:}" sh scripts/monitoring-checks.sh -o "$work/mon-results" 2>&1)
		# What failed, not the steps that passed before it (their lines say "A65:" or "A67:").
		printf '%s\n' "$out" | grep -E '^\s+a6[57]_test.go:[0-9]+: ' | grep -vE ': A6[57]: ' | head -4
		;;
	stack:*)
		out=$(PA_STACK_PART=accounts sh scripts/stack-checks.sh -run "TestA23FirstAdmin|${test#stack:}" 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: (SECURITY FAILURE|FAILED|TEST ERROR)' | head -4
		;;
	pester:*)
		out=$(POWERSHELL_TELEMETRY_OPTOUT=1 "$PA_PWSH" -NoProfile -NonInteractive -File powershell/tests/Invoke-Tests.ps1 -Path "${test#pester:}" 2>&1)
		printf '%s\n' "$out" | grep -E '\[-\]|Expected|Timed out' | head -4
		;;
	a75:*)
		# A75 seeds 4,562,500 pages: longer than the other checks' limit.
		out=$(PAGERARCHIVE_A75=1 go test -p 1 -count=1 -timeout 60m -v -run "${test#a75:}" ./internal/acceptance/ 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: ' | head -24
		;;
	a76:*)
		out=$(sh scripts/a76-checks.sh -run "${test#a76:}" 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: ' | grep -vE ': map\[|: killed part-way|: telemetry exported' | head -6
		;;
	a77:*)
		out=$(sh scripts/a77.sh -o "$work/a77-results" 2>&1)
		printf '%s\n' "$out" | grep -E '^FAIL: |^\s+a77_test.go:[0-9]+: ' | grep -vE ': A77: ' | head -6
		;;
	e2e:*)
		out=$(sh scripts/e2e.sh --project=chromium -g "${test#e2e:}" 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+Error: |^\s+(Expected|Received)|^\s+[0-9]+ (passed|failed)' | head -5
		;;
	esac
	if ! printf '%s\n' "$out" | grep -qE '^(--- FAIL|FAIL)|failed [1-9]|Passed [0-9]+, failed [1-9]|^\s+[0-9]+ failed$'; then
		echo "    -> SURVIVED: no test failed. The run ended with:"
		printf '%s\n' "$out" | tail -n 5 | sed 's/^/       /'
		survived=1
	elif [ -n "$must" ] && ! printf '%s\n' "$out" | grep -qF "$must"; then
		echo "    -> SURVIVED: the test fails, but not with \"$must\""
		survived=1
	else
		echo "    -> the test fails: the check is guarded"
	fi
	cp "$work/.orig" "$file"
	[ -z "$file2" ] || cp "$work/.orig2" "$file2"
}

S=internal/server/powershell.go
mutate "audience check off" $S go:TestA53PowerShellTokens \
	'config := &oidc.Config{ClientID: p.ClientID}' 'config := &oidc.Config{ClientID: p.ClientID, SkipClientIDCheck: true}'
mutate "issuer check off" $S go:TestA53PowerShellTokens \
	'config := &oidc.Config{ClientID: p.ClientID}' 'config := &oidc.Config{ClientID: p.ClientID, SkipIssuerCheck: true}'
mutate "expiry check off" $S go:TestA53PowerShellTokens \
	'config := &oidc.Config{ClientID: p.ClientID}' 'config := &oidc.Config{ClientID: p.ClientID, SkipExpiryCheck: true}'
mutate "access-token check off (azp and scope)" $S go:TestA53PowerShellTokens \
	' || c.Azp != s.PowerShell.ClientID || c.Scope == ""' ''
mutate "admins-only check off (R62)" $S go:TestA53PowerShellTokens \
	'if u.Role != accounts.RoleAdmin {' 'if false && u.Role != accounts.RoleAdmin {'
mutate "status check off" $S go:TestA53PowerShellTokens \
	'if u.Status != "active" {' 'if false && u.Status != "active" {'
mutate "token and cookie together allowed" internal/server/auth.go go:TestA53PowerShellTokens \
	'if err == nil {
				writeJSON(w, http.StatusBadRequest' 'if false {
				writeJSON(w, http.StatusBadRequest'

# M9 (ADR 0010 §4): live streams follow account changes and token expiry.
mutate "a role change doesn't close live streams" internal/accounts/accounts.go go:TestM9RoleChangeClosesStreams \
	'if err == nil && changed {' 'if false && err == nil && changed {'
mutate "a bearer stream outlives its token" internal/server/browse.go go:TestM9BearerStreamEndsAtTokenExpiry \
	'if exp, ok := tokenExpiry(r); ok {' 'if exp, ok := tokenExpiry(r); false && ok {'

# M10 (ADR 0011): template icons, and admins only on the template endpoints.
I=internal/server/icons.go
C=internal/icon/icon.go
mutate "the icon's size is checked after reading the whole body" $I go:TestIconSizeLimitBeforeReading \
	'	if r.ContentLength > icon.MaxBytes {
		s.iconRefused(w, r, id, http.StatusRequestEntityTooLarge, icon.TooLarge())
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, icon.MaxBytes))' '	data, err := io.ReadAll(r.Body)'
mutate "the icon's type is taken from its Content-Type when its bytes aren't an image" $I go:TestA57IconsShown \
	'	png, err := icon.Convert(data)
' '	png, err := icon.Convert(data)
	if err != nil && r.Header.Get("Content-Type") == "image/png" {
		png, err = data, nil
	}
'
mutate "SVG isn't refused with its own reason" $C go:TestA57IconsShown \
	'case strings.Contains(head, "<svg"):' 'case false && strings.Contains(head, "<svg"):'
mutate "the declared size isn't checked before decoding" $C go:TestA57IconsShown \
	'if r := checkSize(c.Width, c.Height); r != nil {' 'if r := checkSize(c.Width, c.Height); false && r != nil {'
mutate "the uploaded file is stored as it came, not encoded afresh" $C go:TestA58StoredNotLinked \
	'	return buf.Bytes(), nil' '	return data, nil'
S=internal/server/server.go
for h in createTemplate editTemplate deleteTemplate putTemplateIcon deleteTemplateIcon; do
	mutate "admins only off: $h" $S go:TestA56AdminManagesTemplates "s.adminOnly(s.$h)," "s.$h,"
done
mutate "the server fetches an icon sent as a URL" $I go:TestA58StoredNotLinked \
	'	png, err := icon.Convert(data)
' '	if len(data) > 4 && string(data[:4]) == "http" {
		if resp, err := http.Get(string(data)); err == nil {
			data, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}
	png, err := icon.Convert(data)
'

# M11 (ADR 0012): the print sheet. The API's admin-only details, then the
# browser's checks, through scripts/e2e.sh in Chromium (each run starts its
# own database and API).
mutate "admin-only details sent to a user from the print sheet's endpoint" internal/server/browse.go go:TestM11MessagesByID \
	'found[m.ID] = s.toMessage(m, admin)' 'found[m.ID] = s.toMessage(m, true)'
if [ -d "$src/web/node_modules" ] && command -v docker >/dev/null; then
	ln -s "$src/web/node_modules" web/node_modules
	mutate "message text stored in localStorage" web/src/lib/PrintTick.svelte 'e2e:A60 print sheet$' \
		'		if (!tick(id, e.currentTarget.checked))' '		localStorage.setItem("pagerarchive:print-sheet-text:" + id, e.currentTarget.closest("article")?.querySelector(".text")?.textContent ?? "");
		if (!tick(id, e.currentTarget.checked))'
	# Ticking only on the print page (owner, 2026-10-30).
	mutate "a tick box back in the message list" web/src/routes/+page.svelte 'e2e:A60 print sheet$' \
		'<MessageList {query} />' '<MessageList {query} pick />'
	mutate "a tick box back in the job view" 'web/src/routes/jobs/[job]/+page.svelte' 'e2e:A60 print sheet$' \
		"<MessageRow {m} tz={meta?.time_zone ?? 'UTC'} />" "<MessageRow {m} tz={meta?.time_zone ?? 'UTC'} pick />"
	mutate "the print page's search offers flag filters to a non-admin" web/src/lib/Filters.svelte 'e2e:print page search matches' \
		"{#if meta.role === 'admin'}" "{#if meta.role === 'admin' || page.url.pathname === '/sheet'}"
	mutate "the storage key not per user" web/src/lib/sheet.svelte.ts 'e2e:print sheet is per user' \
		'const storageKey = (userId: number) => `pagerarchive:print-sheet:${userId}`;' 'const storageKey = (userId: number) => "pagerarchive:print-sheet";'
	mutate "the sheet sorted by time, newest first, not in the order added" web/src/routes/sheet/+page.svelte 'e2e:A60 print sheet$' \
		'(id in fetched ? [fetched[id]] : [])));' '(id in fetched ? [fetched[id]] : [])).toSorted((x, y) => y.sent_at.localeCompare(x.sent_at)));'
	mutate "break-inside removed, so a message splits" web/src/routes/sheet/+page.svelte 'e2e:A60 thirty messages' \
		'			break-inside: avoid;
			page-break-inside: avoid;
' ''
	mutate "the header and navigation shown in the printout" web/src/routes/+layout.svelte 'e2e:A60 print sheet$' \
		'		.skip,
		.top,
' '		.skip,
'
else
	echo "=== the print sheet's browser mutations skipped: no web/node_modules or no Docker"
fi

if [ -n "$PA_PWSH" ]; then
	P=powershell/PagerArchive/Private
	mutate "DPAPI save allowed off Windows" $P/SignInStore.ps1 pester:powershell/tests/SignIn.Tests.ps1 \
		'    if (-not $IsWindows) {
        throw (New-PaErrorRecord -ErrorId '"'"'dpapi_windows_only'"'"' -Message '"'"'Saving' '    if ($false) {
        throw (New-PaErrorRecord -ErrorId '"'"'dpapi_windows_only'"'"' -Message '"'"'Saving'
	mutate "no vault saves to the DPAPI file off Windows" $P/SignInStore.ps1 pester:powershell/tests/SignIn.Tests.ps1 \
		'    if ($IsWindows) {
        return @{ Store = '"'"'dpapi'"'"'' '    if ($true) {
        return @{ Store = '"'"'dpapi'"'"''
	mutate "plain HTTP allowed without -AllowUnencryptedAuthentication" $P/Connection.ps1 pester:powershell/tests/Module.Tests.ps1 \
		'if ($Uri.Scheme -eq '"'"'http'"'"' -and -not $AllowUnencryptedAuthentication) {' 'if ($false) {'
	mutate "the watch retries after deactivation instead of stopping" powershell/PagerArchive/Public/Watch-PagerArchiveMessage.ps1 pester:powershell/tests/Watch.Tests.ps1 \
		"-in @('ConnectionError', 'LimitsExceeded')" "-in @('ConnectionError', 'LimitsExceeded', 'AuthenticationError')"
	mutate "the watch sends its token over plain HTTP without -AllowUnencryptedAuthentication" $P/EventStream.ps1 pester:powershell/tests/Watch.Tests.ps1 \
		'-AllowUnencryptedAuthentication:([bool]$Connection.AllowUnencrypted)' '-AllowUnencryptedAuthentication'
	mutate "the watch accepts any certificate" $P/EventStream.ps1 pester:powershell/tests/Watch.Tests.ps1 \
		'$state.Client = [System.Net.Http.HttpClient]::new()' '$state.Client = [System.Net.Http.HttpClient]::new([System.Net.Http.HttpClientHandler]@{ ServerCertificateCustomValidationCallback = [System.Net.Http.HttpClientHandler]::DangerousAcceptAnyServerCertificateValidator })'
	# M11 (ADR 0012): a Ctrl+C that PowerShell loses still ends the watch.
	mutate "the watch closes its stream in a finally block, where PowerShell loses a Ctrl+C" powershell/PagerArchive/Public/Watch-PagerArchiveMessage.ps1 pester:powershell/tests/Watch.Tests.ps1 \
		'                Close-PaEventStream -State $stream
                $stream = $null
                if (-not $failure) {' '                try { $null = $failure } finally { Close-PaEventStream -State $stream }
                $stream = $null
                if (-not $failure) {'
	mutate "the watch's waits ignore its stop token" $P/EventStream.ps1 pester:powershell/tests/Watch.Tests.ps1 \
		'        if ($Stop.IsCancellationRequested) {' '        if ($false) {'
	mutate "the wait before reconnecting ignores the stop token" $P/EventStream.ps1 pester:powershell/tests/Watch.Tests.ps1 \
		'    if (-not $Stop.CanBeCanceled) {' '    if ($true) {'
	W=powershell/PagerArchive/Public/Watch-PagerArchiveMessage.ps1
	mutate "the watch doesn't pass its stop token when connecting" $W pester:powershell/tests/Watch.Tests.ps1 \
		'(ConvertTo-PaQueryString -Query $query)) -Stop $stop' '(ConvertTo-PaQueryString -Query $query))'
	mutate "the watch doesn't pass its stop token when waiting to reconnect" $W pester:powershell/tests/Watch.Tests.ps1 \
		'Wait-PaRetry -Seconds $delay -Stop $stop' 'Wait-PaRetry -Seconds $delay'
	mutate "the watch passes its stop token to no wait (both calls)" $W pester:powershell/tests/Watch.Tests.ps1 \
		' -Stop $stop' ''
	# The casing check that replaces PSUseCorrectCasing's command check.
	mutate "a command and a parameter written in the wrong case" $P/SignInStore.ps1 pester:powershell/tests/Meta.Tests.ps1 \
		'Test-Path -LiteralPath' 'test-path -literalPath'
	mutate "an icon download has no size limit while downloading" $P/Icon.ps1 pester:powershell/tests/Icon.Tests.ps1 \
		'            if ($bytes.Length -gt $script:PaIconMaxBytes) {' '            if ($false) {'
	mutate "an icon download has no time limit" $P/Icon.ps1 pester:powershell/tests/Icon.Tests.ps1 \
		'[System.Threading.CancellationTokenSource]::new($script:PaIconDownloadTimeout)' '[System.Threading.CancellationTokenSource]::new()'
else
	echo "=== the module's mutations skipped: no pwsh (set PA_PWSH)"
fi

# M12 (ADR 0013): telemetry holds nothing private, never blocks a page, and
# the example stack tells when a client stops. Each is checked where it's
# made (TestM12SourceSpans, before the export allowlist), as exported (A63)
# or end to end (A62, A64, A65).
T=internal/telemetry
mutate "the real path and query as a request span's name" $T/http.go go:'TestA63NothingPrivate|TestM12SourceSpans' \
	'span.SetName(route)' 'span.SetName(method(r) + " " + r.URL.RequestURI())'
mutate "SQL arguments recorded on database spans" internal/store/trace.go go:TestM12SourceSpans \
	'attribute.String("db.operation.name", name)))' \
	'attribute.String("db.operation.name", name), attribute.StringSlice("db.query.parameters", func() (out []string) {
			for _, a := range data.Args {
				if s, ok := a.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}())))'
mutate "an email address on a signed-in request's span" internal/server/auth.go go:TestM12SourceSpans \
	'telemetry.SetUser(r.Context(), u.ID, u.Role)' 'telemetry.SetUser(r.Context(), u.ID, u.Role+" "+*u.Email)'
mutate "the allowlist removed, and a new attribute carrying page text" $T/filter.go go:TestA63NothingPrivate \
	'		if kv, ok := keep(keys, kv); ok {
			out = append(out, kv)' '		if true {
			out = append(out, kv)' \
	internal/server/server.go \
	'span.SetAttributes(attribute.String("pagerarchive.ingest.outcome", string(outcome)))' \
	'span.SetAttributes(attribute.String("pagerarchive.ingest.outcome", string(outcome)), attribute.String("page.text", req.Text))'
mutate "a browser request's traceparent continued" $T/http.go go:TestM12SourceSpans \
	'ctx, st.span = tracer.Start(ctx, "HTTP "+method(r), trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(st.start))' \
	'ctx, st.span = tracer.Start(TraceContext.Extract(ctx, propagation.HeaderCarrier(r.Header)), "HTTP "+method(r), trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(st.start))'
mutate "the span exporter made synchronous, so a hung collector blocks ingest" $T/telemetry.go go:'TestA64CollectorDown/hung' \
	'sdktrace.WithBatcher(&spanExporter{' 'sdktrace.WithSyncer(&spanExporter{'
mutate "PRINT_PAGES output sent to OTLP" internal/client/client.go go:TestA63NothingPrivate \
	'			fmt.Fprintf(rd.Stdout, "%s %s %s\n", now.Format(time.RFC3339), rec.Capcode, rec.Text)' \
	'			fmt.Fprintf(rd.Stdout, "%s %s %s\n", now.Format(time.RFC3339), rec.Capcode, rec.Text)
			rd.Log.InfoContext(ctx, rec.Text)'
mutate "the client doesn't send traceparent" internal/client/client.go go:TestA62PageTrace \
	'	telemetry.TraceContext.Inject(ctx, propagation.HeaderCarrier(req.Header))' \
	'	_ = propagation.HeaderCarrier(req.Header)'
mutate "a span key added to the app's allowlist but not to Alloy's" $T/filter.go telemetry:TestAlloyAllowlistMatches \
	'	"pagerarchive.reparse.messages": intKind,' \
	'	"pagerarchive.reparse.messages": intKind,
	"pagerarchive.reparse.batches":  intKind,'
if command -v docker >/dev/null; then
	mutate "the \"client not receiving\" alert rule's query broken" deploy/monitoring/grafana/provisioning/alerting/rules.yaml mon:TestA65 \
		'(pagerarchive_client_last_seen_timestamp_seconds{job="pagerarchive"})' '(pagerarchive_client_last_seen_seconds{job="pagerarchive"})'
fi

# M12 addition (owner, 2026-10-31): radio health (R74a), the "radio stalled"
# alert and stopped clients on the dashboard (R76, A67), and a failed
# delivery's cause on its span (R71).
mutate "a failed delivery's cause dropped from its span" internal/integrations/trace.go unit:internal/integrations:'TestFailureCauses|TestTooOldCause' \
	'	cause := res.cause
' '	cause := ""
'
R=internal/radio
mutate "squelch added to rtl_fm's arguments" $R/radio.go unit:internal/radio:TestNoSquelch \
	'"-s", fmt.Sprint(SampleRate), "-p", s.PPM}' '"-s", fmt.Sprint(SampleRate), "-l", "70", "-p", s.PPM}'
mutate "rtl_fm's message text exported as the kind label" $R/health.go unit:internal/client:TestRadioExportsKindsOnly \
	'			h.count(k)' '			h.count(line)'
mutate "measuring made blocking: each chunk handed to the metrics side on an unbuffered channel" $R/health.go unit:internal/client:TestRadioMeasuringNeverBlocks \
	'func (h *Health) measure(b []byte) {' '// Measured hands each chunk to the metrics side, which takes it when it collects.
var Measured = make(chan []byte)

func (h *Health) measure(b []byte) { Measured <- b }

// Measure is what measure did.
func (h *Health) Measure(b []byte) {' \
	internal/client/metrics.go \
	'			o.ObserveInt64(audio, health.Bytes())' '			select {
			case b := <-radio.Measured:
				health.Measure(b)
			default:
			}
			o.ObserveInt64(audio, health.Bytes())'
if command -v docker >/dev/null; then
	mutate "the \"radio stalled\" alert rule's query broken" deploy/monitoring/grafana/provisioning/alerting/rules.yaml mon:TestA67 \
		'(label_replace(pagerarchive_client_radio_silent_seconds,' '(label_replace(pagerarchive_client_radio_silence_seconds,'
	mutate "the clients panel back on instant queries only" deploy/monitoring/grafana/dashboards/clients.json mon:TestA67 \
		'last_over_time(' '(' \
		deploy/monitoring/grafana/dashboards/clients.json '[$__range:1m])' ')'
else
	echo "=== the alert rules' and dashboard's mutations skipped: no Docker"
fi

# ADR 0014: the account checks decide on Authentik's real answer, and the
# client exports its last line, which says why it stopped (R73).
mutate "the flow executor's reply to a 500 is an empty challenge, its status and body dropped (as before)" internal/stacktest/flow_test.go unit:internal/stacktest:TestRepliesThatAreNotAnswers \
	'	_ = json.Unmarshal(data, &c)' '	if resp.StatusCode == http.StatusInternalServerError {
		return challenge{}, false
	}
	_ = json.Unmarshal(data, &c)'
mutate "the executor retries any reply, not only transient ones" internal/stacktest/flow_test.go unit:internal/stacktest:TestOnlyTransientRepliesAreRetried \
	'	return c.err != nil || c.status >= 500' '	return true'
mutate "the executor resends a password or code without asking whether Authentik counted it" internal/stacktest/flow_test.go unit:internal/stacktest:TestOnlyTransientRepliesAreRetried \
	'			if n := b.failures(); n != before {' '			if n := b.failures(); false {'
mutate "the executor sends one request up to 10 times, not 3" internal/stacktest/flow_test.go unit:internal/stacktest:TestOnlyTransientRepliesAreRetried \
	'const transientTries = 3' 'const transientTries = 10'
mutate "any access-denied counts as the lock's refusal, whatever its message" internal/stacktest/outcome_test.go unit:internal/stacktest:TestRepliesThatAreNotAnswers \
	'case c.Component == "ak-stage-access-denied" && strings.Contains(text, tooManyAttempts):' 'case c.Component == "ak-stage-access-denied":'
# R33a's text terms (owner, 2026-11-02; ADR 0015): every term required, or:
# an alternative, a quoted phrase one term.
mutate "text terms: any one term is enough (AND made OR)" internal/search/search.go go:TestSearchTextTerms \
	'	p.QAll = likeContains(terms.All)
	p.QAny = likeContains(terms.Any)' '	p.QAll = []string{}
	p.QAny = likeContains(append(terms.All, terms.Any...))'
mutate "text terms: or: is ignored (an or: term is one more required term)" internal/search/text.go unit:internal/search:TestParseText \
	'		if or {' '		if false && or {'
mutate "text terms: quotes are ignored (a phrase is split into words)" internal/search/text.go unit:internal/search:TestParseText \
	"		if i < len(s) && s[i] == '\"' {" '		if false {'
# R33b's search speed (ADR 0017): each term is matched field by field,
# whatever the index found; the index is what makes A75 fast, and the second
# try with it is what makes terms that are never together fast.
mutate "search: a term matches across two extra fields (the values joined)" internal/store/db/queries.sql.go go:TestSearchFieldBoundary \
	'EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE term.p)' "(SELECT string_agg(e.value, '') FROM jsonb_each_text(m.extra) e) ILIKE term.p"
mutate "search: the search index isn't built" internal/store/migrations/00013_search_speed.sql a75:TestA75SearchSpeed \
	'CREATE INDEX CONCURRENTLY messages_search_trgm_idx ON messages USING gin (message_search_text(text, extra) gin_trgm_ops);' ''
mutate "search: no other try in time (the first try runs for an hour)" internal/store/search.go a75:TestA75SearchSpeed \
	'SearchFirstTry = 100 * time.Millisecond' 'SearchFirstTry = time.Hour'
# A first run after a cold start (A75): a first try the disk has held up isn't
# given up for a try that reads a third of the table.
must="the first run after a cold start took"
mutate "search: a first try is given up at 250 ms for the second try, as before" internal/store/search.go a75:TestA75SearchSpeed \
	'	if !s.SecondTryOnly {
		rows, complete, err := s.searchBoundedTry(ctx, p)
		if err != nil || complete {
			return rows, err
		}
	}
	if !s.SecondTryOnly && !s.BoundedTryOnly {
		rows, err := s.searchFirstTry(ctx, p, searchFirstTryAgain)
		if !searchTimedOut(ctx, err) {
			return rows, err
		}
	}
' '' internal/store/search.go '		return searchFirstTryOr
	}
	return SearchFirstTry' '		return 250 * time.Millisecond
	}
	return 250 * time.Millisecond'
mutate "search: the bounded try keeps its order, so it reads every match" internal/store/search.go a75:TestA75SearchSpeed \
	'strings.Replace(sql, searchOrderBy, "", 1)' 'strings.Replace(sql, searchOrderBy, searchOrderBy, 1)'
mutate "search: alias: values that name every alias still go to the planner" internal/store/search.go a75:TestA75SearchSpeed \
	'if len(ids) < aliases {' 'if len(ids) <= aliases {'
must=""
mutate "search: the bounded try's answer isn't in the search's order" internal/store/search.go go:TestSearchBoundedTry \
	'if c := b.SentAt.Compare(a.SentAt); c != 0 {' 'if c := a.SentAt.Compare(b.SentAt); c != 0 {'
mutate "search: the bounded try's stopped short answer counts as all of them" internal/store/search.go go:TestSearchBoundedTry \
	'if err != nil || len(rows) >= searchBoundedRows {' 'if err != nil {'
mutate "search: the second try looks up alias: values with case" internal/store/db/queries.sql.go go:TestSearchSecondTryAliases \
	'WHERE (cardinality($1::text[]) = 0 OR name ILIKE ANY($1::text[]))' 'WHERE (cardinality($1::text[]) = 0 OR name LIKE ANY($1::text[]))'
# R83-R83e, the import (ADR 0018): quiet, redacted, safe to repeat, and no
# line's text in anything it reports. The tests run with the live path's service
# (TestM16*), and through the command (TestA76FileChecks).
mutate "import: imported pages queue integration deliveries" internal/importer/importer.go go:TestM16Quiet \
	'	quiet.OnStored = nil
' ''
mutate "import: redaction is skipped" internal/importer/importer.go go:TestM16SamePathAsALivePage \
	'		if prep.Dropped {
			s.rep.Ignored++' '		prep.Text = page.text
		if prep.Dropped {
			s.rep.Ignored++'
mutate "import: source_id is ignored" internal/importer/importer.go go:TestM16SafeToRepeat \
	'	for _, id := range found {
		seen[id] = true
	}
' '	_ = found
'
mutate "import: a refused line's text is logged" internal/importer/importer.go go:TestM16RefusedLines \
	'fmt.Fprintf(s.im.Report, "line %d: %s\n", s.rep.Lines, reason)' 'fmt.Fprintf(s.im.Report, "line %d: %s: %s\n", s.rep.Lines, reason, line)'
mutate "import: a refused line's reason is the decoder's error, which quotes the line" internal/importer/importer.go go:TestM16RefusedLines \
	'	at, err := time.Parse(time.RFC3339, *rec.ReceivedAt)
	if err != nil {
		return checked{}, reasonTime
	}' '	at, err := time.Parse(time.RFC3339, *rec.ReceivedAt)
	if err != nil {
		return checked{}, err.Error()
	}'
mutate "import: the file's order decides (no sort)" internal/importer/importer.go go:TestM16OrderOfTheFile \
	'ORDER BY received_at, capcode COLLATE "C", text COLLATE "C", source_id COLLATE "C"`, after, upto)' 'ORDER BY ctid`, after, upto)'
mutate "import: a watch's catch-up sends imported pages" internal/store/db/queries.sql.go go:TestM16Quiet \
	'SELECT DISTINCT message_id FROM receptions WHERE message_id > $1 ORDER BY message_id LIMIT $2::int' 'SELECT id FROM messages WHERE id > $1 ORDER BY id LIMIT $2::int'
if command -v docker >/dev/null; then
	mutate "import, through the command: imported pages queue integration deliveries" internal/importer/importer.go a76:TestA76FileChecks \
		'	quiet.OnStored = nil
' '	quiet.OnStored = func(ctx context.Context, q *db.Queries, id int64) error {
		now := time.Now()
		return q.InsertDelivery(ctx, db.InsertDeliveryParams{IntegrationID: 1, MessageID: id, Status: "pending", NextAttemptAt: &now})
	}
'
	mutate "import, through the command: redaction is skipped" internal/importer/importer.go a76:TestA76FileChecks \
		'		if prep.Dropped {
			s.rep.Ignored++' '		prep.Text = page.text
		if prep.Dropped {
			s.rep.Ignored++'
	mutate "import, through the command: source_id is ignored" internal/importer/importer.go a76:TestA76FileChecks \
		'	for _, id := range found {
		seen[id] = true
	}
' '	_ = found
'
	mutate "import, through the command: a refused line's text is logged" internal/importer/importer.go a76:TestA76FileChecks \
		'fmt.Fprintf(s.im.Report, "line %d: %s\n", s.rep.Lines, reason)' 'fmt.Fprintf(s.im.Report, "line %d: %s: %s\n", s.rep.Lines, reason, line)'
else
	echo "=== the import's mutations through the command skipped: no Docker"
fi
mutate "the client logs why it stopped after telemetry has shut down" cmd/pagerarchive-client/main.go go:TestR73EveryClientLineExported \
	'			log.Error("client stopped", "error", err.Error())
			err = stopped{err}
' ''
if command -v docker >/dev/null; then
	must="SECURITY FAILURE"
	mutate "the lock allows a 6th attempt (LIMIT 6)" deploy/authentik/blueprints/pagerarchive.yaml stack:TestA33WithinOneFlow \
		'LIMIT, WINDOW = 5, timedelta(minutes=15)' 'LIMIT, WINDOW = 6, timedelta(minutes=15)'
	must=""
else
	echo "=== the lock's mutation skipped: no Docker"
fi

# M17 (ADR 0019): installing and upgrading with no checkout.
V=$(tr -d ' \r\n' <VERSION)
V=${V%.*}
mutate "compose.yaml pulls an image that isn't ours or known upstream" compose.yaml unit:.:TestR84ImagesAreOursOrKnownUpstream \
	'image: postgis/postgis:18-3.6' 'image: example.org/someone/postgis:18-3.6'
mutate "the monitoring stack pulls a project-built image instead of upstream's" deploy/monitoring/compose.yaml unit:.:TestR84ImagesAreOursOrKnownUpstream \
	'image: grafana/grafana:13.2' 'image: ghcr.io/rikiku/pagerarchive-grafana:13.2'
mutate "an upstream image has a patch number" compose.yaml unit:.:TestA79ImagesAreNamedByMajorMinor \
	'ghcr.io/goauthentik/server:2026.5' 'ghcr.io/goauthentik/server:2026.5.5'
mutate "our image has a patch number" compose.yaml unit:.:TestA79ImagesAreNamedByMajorMinor \
	"ghcr.io/rikiku/pagerarchive:$V" "ghcr.io/rikiku/pagerarchive:$V.0"
mutate "an image has a digest" compose.yaml unit:.:TestA79ImagesAreNamedByMajorMinor \
	'postgis/postgis:18-3.6' 'postgis/postgis:18-3.6@sha256:4a43e3c3e5d55b9208240fc71db543b79c638766280c625a425bd0a2390989c4'
mutate "an image moves further than a minor version" deploy/monitoring/compose.yaml unit:.:TestA79ImagesAreNamedByMajorMinor \
	'grafana/grafana:13.2' 'grafana/grafana:latest'
mutate "the release doesn't attach compose.yaml (the script)" scripts/ci/release-assets.sh unit:.:TestR84ReleaseAttachesTheFiles \
	'cp compose.yaml "$out/compose.yaml"' ':'
mutate "the release doesn't attach compose.yaml (the job)" .github/workflows/ci.yml unit:.:TestR84ReleaseAttachesTheFiles \
	'gh release upload "$TAG" dist/* --clobber' 'gh release upload "$TAG" dist/pagerarchive-client-* --clobber'
mutate "the files are attached before the images are on GHCR" .github/workflows/ci.yml unit:.:TestR84ReleaseAttachesTheFiles \
	'needs: [release, publish-server, publish-client-manifest]' 'needs: [release]'
mutate "the release is the latest before its files are attached" .github/workflows/ci.yml unit:.:TestR84ReleaseAttachesTheFiles \
	'--verify-tag --latest=false --title' '--verify-tag --title'
mutate "the attached compose.yaml takes the image from latest" compose.yaml unit:.:TestR84aComposeNamesThisRelease \
	"ghcr.io/rikiku/pagerarchive:$V" 'ghcr.io/rikiku/pagerarchive:latest'
mutate "a file the stack mounts isn't in the archive" scripts/ci/release-assets.sh unit:.:TestR84MountsComeFromTheArchive \
	'deploy/authentik deploy/postgres deploy/monitoring/alloy' 'deploy/postgres deploy/monitoring/alloy'
mutate "a changed Authentik file doesn't recreate Authentik" deploy/authentik/blueprints/pagerarchive.yaml unit:.:TestR84aChangedConfigRecreatesItsService \
	'LIMIT, WINDOW = 5, timedelta(minutes=15)' 'LIMIT, WINDOW = 4, timedelta(minutes=15)'
mutate "a changed alert rule doesn't recreate Grafana" deploy/monitoring/grafana/provisioning/alerting/rules.yaml unit:.:TestR84aChangedConfigRecreatesItsService \
	'[1800]' '[1801]'
mutate "the example built into the image is used without the warning" cmd/pagerarchive/main.go a77: \
	'	if example {
		log.Warn(' '	if example && false {
		log.Warn('
mutate "the metrics role is left for a hand step (the function)" internal/authentik/locks.go go:TestM17MetricsRoleOnStart \
	'	if !exists {
		if _, err := conn.Exec(ctx, `CREATE ROLE pagerarchive_metrics`); err != nil {
			return err
		}
	}' '	if !exists {
		return nil
	}'
mutate "the metrics role is left for a hand step (the API doesn't call it)" cmd/pagerarchive/main.go a77: \
	'if err := authentik.EnsureLocksRole(ctx, dbURL, pw); err != nil {' 'if err := error(nil); err != nil {'
if [ -n "${PA_MUT_ONLY:-}" ]; then
	exit $survived
fi

echo "=== unmutated"
go test -count=1 -run 'TestR84|TestA79|TestVersion' . 2>&1 | tail -3
go test -p 1 -count=1 -run 'TestA53PowerShellTokens|TestM9|TestA56|TestA57|TestA58|TestIconSize|TestM11|TestA62|TestA63|TestA64|TestM12|TestM16|TestM17|TestR73|TestSearchFieldBoundary|TestSearchSecondTryAliases|TestSearchBoundedTry' ./internal/acceptance/ 2>&1 | tail -20
! command -v docker >/dev/null || sh scripts/a76-checks.sh 2>&1 | grep -E '^(ok|FAIL|--- (PASS|FAIL)|API log lines)'
! command -v docker >/dev/null || sh scripts/a77.sh -o "$work/a77-results" 2>&1 | grep -E '^(ok|FAIL|=== A77|--- (PASS|FAIL))'
go test -count=1 ./internal/telemetry/ ./internal/integrations/ ./internal/radio/... ./internal/client/ ./internal/stacktest/ 2>&1 | tail -5
PAGERARCHIVE_A75=1 go test -p 1 -count=1 -timeout 60m -v -run TestA75SearchSpeed ./internal/acceptance/ 2>&1 | grep -E '^(--- |ok|FAIL)|m13a_test.go'
! command -v docker >/dev/null || PA_STACK_PART=accounts sh scripts/stack-checks.sh -run 'TestA23FirstAdmin|TestA33' 2>&1 | grep -E '^(ok|FAIL|--- (PASS|FAIL))'
! command -v docker >/dev/null || sh scripts/monitoring-checks.sh -o "$work/mon-results" 2>&1 | grep -E '^(ok|FAIL|--- (PASS|FAIL))'
[ ! -L web/node_modules ] || sh scripts/e2e.sh --project=chromium -g 'A60|print sheet|print page' 2>&1 | grep -E '^\s+[0-9]+ (passed|failed)'
[ -z "$PA_PWSH" ] || "$PA_PWSH" -NoProfile -NonInteractive -File powershell/tests/Invoke-Tests.ps1 2>&1 | tail -1
exit $survived
