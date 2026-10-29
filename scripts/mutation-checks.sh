#!/bin/sh
# Mutation proof for the security checks (M8, M9, M10) and the print sheet
# (M11): break one check at a time in a throwaway copy of the repository, run
# the test that guards it, and show the test failing. Every mutation must
# fail its test; the unmutated copy must pass.
#
# usage: scripts/mutation-checks.sh
# needs: Go and PAGERARCHIVE_TEST_DATABASE_URL (as for go test); for the
# print sheet's browser checks, what scripts/e2e.sh needs (Docker, Node with
# web/ dependencies installed, Playwright's Chromium), and no other e2e.sh
# running; for the module's checks pwsh (PA_PWSH, default: pwsh on PATH)
# with Pester 5.9.1 on PSModulePath. Linux only: the module's checks are the
# off-Windows ones.
set -u
src=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd "$src" && tar -cf - --exclude=.git --exclude=web/node_modules .) | (cd "$work" && tar -xf -)
cd "$work" || exit 1
PA_PWSH=${PA_PWSH:-$(command -v pwsh || true)}
survived=0

# mutate <name> <file> <test> <old> <new>: replace exact text, run the test,
# and restore. <test> is "go:<regexp>" or "pester:<file>".
mutate() {
	name=$1 file=$2 test=$3 old=$4 new=$5
	cp "$file" "$work/.orig"
	OLD=$old NEW=$new perl -0pi -e '$n = s/\Q$ENV{OLD}\E/$ENV{NEW}/g; END { exit($n ? 0 : 1) }' "$file" || {
		echo "=== NOT APPLIED: $name"
		survived=1
		return
	}
	echo "=== mutation: $name ($file)"
	diff "$work/.orig" "$file" | sed 's/^/    /'
	case $test in
	go:*)
		out=$(go test -p 1 -count=1 -v -run "${test#go:}" ./internal/acceptance/ 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+[a-z0-9_]+_test.go:[0-9]+: ' | grep -vE ': 401 invalid_token$|^\s+[a-z0-9_]+_test.go:[0-9]+: (A53: user B|ADR 0010)' | head -4
		;;
	pester:*)
		out=$("$PA_PWSH" -NoProfile -NonInteractive -File powershell/tests/Invoke-Tests.ps1 -Path "${test#pester:}" 2>&1)
		printf '%s\n' "$out" | grep -E '\[-\]|Expected|Timed out' | head -4
		;;
	e2e:*)
		out=$(sh scripts/e2e.sh --project=chromium -g "${test#e2e:}" 2>&1)
		printf '%s\n' "$out" | grep -E '^\s+Error: |^\s+(Expected|Received)|^\s+[0-9]+ (passed|failed)' | head -5
		;;
	esac
	if printf '%s\n' "$out" | grep -qE '^(--- FAIL|FAIL)|failed [1-9]|Passed [0-9]+, failed [1-9]|^\s+[0-9]+ failed$'; then
		echo "    -> the test fails: the check is guarded"
	else
		echo "    -> SURVIVED: no test failed"
		survived=1
	fi
	cp "$work/.orig" "$file"
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
	mutate "message text stored in localStorage" web/src/lib/MessageRow.svelte 'e2e:A60 print sheet$' \
		'		if (!tick(m.id, e.currentTarget.checked))' '		localStorage.setItem("pagerarchive:print-sheet-text:" + m.id, m.display_text);
		if (!tick(m.id, e.currentTarget.checked))'
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
	mutate "an icon download has no size limit while downloading" $P/Icon.ps1 pester:powershell/tests/Icon.Tests.ps1 \
		'            if ($bytes.Length -gt $script:PaIconMaxBytes) {' '            if ($false) {'
	mutate "an icon download has no time limit" $P/Icon.ps1 pester:powershell/tests/Icon.Tests.ps1 \
		'[System.Threading.CancellationTokenSource]::new($script:PaIconDownloadTimeout)' '[System.Threading.CancellationTokenSource]::new()'
else
	echo "=== the module's mutations skipped: no pwsh (set PA_PWSH)"
fi

echo "=== unmutated"
go test -p 1 -count=1 -run 'TestA53PowerShellTokens|TestM9|TestA56|TestA57|TestA58|TestIconSize|TestM11' ./internal/acceptance/ 2>&1 | tail -1
[ ! -L web/node_modules ] || sh scripts/e2e.sh --project=chromium -g 'A60|print sheet' 2>&1 | grep -E '^\s+[0-9]+ (passed|failed)'
[ -z "$PA_PWSH" ] || "$PA_PWSH" -NoProfile -NonInteractive -File powershell/tests/Invoke-Tests.ps1 2>&1 | tail -1
exit $survived
