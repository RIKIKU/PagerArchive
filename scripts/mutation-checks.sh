#!/bin/sh
# Mutation proof for the security checks (M8): break one check at a time in
# a throwaway copy of the repository, run the test that guards it, and show
# the test failing. Every mutation must fail its test; the unmutated copy
# must pass.
#
# usage: scripts/mutation-checks.sh
# needs: Go and PAGERARCHIVE_TEST_DATABASE_URL (as for go test), and for the
# module's checks pwsh (PA_PWSH, default: pwsh on PATH) with Pester 5.9.1 on
# PSModulePath. Linux only: the module's checks are the off-Windows ones.
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
		printf '%s\n' "$out" | grep -E '^\s+[a-z_]+_test.go:[0-9]+: ' | grep -vE ': 401 invalid_token$|^\s+[a-z_]+_test.go:[0-9]+: A53: user B' | head -4
		;;
	pester:*)
		out=$("$PA_PWSH" -NoProfile -NonInteractive -File powershell/tests/Invoke-Tests.ps1 -Path "${test#pester:}" 2>&1)
		printf '%s\n' "$out" | grep -E '^\s*\[-\]|Expected' | head -4
		;;
	esac
	if printf '%s\n' "$out" | grep -qE '^(--- FAIL|FAIL)|failed [1-9]|Passed [0-9]+, failed [1-9]'; then
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
else
	echo "=== the module's mutations skipped: no pwsh (set PA_PWSH)"
fi

echo "=== unmutated"
go test -p 1 -count=1 -run TestA53PowerShellTokens ./internal/acceptance/ 2>&1 | tail -1
[ -z "$PA_PWSH" ] || "$PA_PWSH" -NoProfile -NonInteractive -File powershell/tests/Invoke-Tests.ps1 2>&1 | tail -1
exit $survived
