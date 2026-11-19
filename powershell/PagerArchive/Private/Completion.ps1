# Tab completion for the small lists (R63, owner 2026-11-19): template
# names, agencies, agency profiles and event categories. Every parameter
# that completes has [ArgumentCompleter([PagerArchiveCompleter])] (the
# class is in PagerArchive.psm1), so they all share this cache and these
# rules. Alias names, call signs, capcodes and job numbers aren't
# completed: there are too many.

# Each list's values, when they were fetched, and from which server.
$script:CompletionCache = @{}

# How long a list is used before it's fetched again.
$script:CompletionLifetime = [timespan]::FromMinutes(5)

# How long a completion waits for the server. Tab never hangs for long.
$script:CompletionTimeoutSec = 5

function Get-PaCompletionValue {
    <#
    .SYNOPSIS
    Fetches one list's values from the API, for completion.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # Template, Agency, AgencyProfile or Category.
        [Parameter(Mandatory)]
        [ValidateSet('Template', 'Agency', 'AgencyProfile', 'Category')]
        [string] $List
    )
    $call = @{ Method = 'GET'; TimeoutSec = $script:CompletionTimeoutSec; Verbose = $false; Debug = $false }
    $values = switch ($List) {
        'Template' { @(Invoke-PaApi @call -Path '/api/v1/admin/templates') | ForEach-Object -Process { $_.name } }
        'Agency' { @(Invoke-PaApi @call -Path '/api/v1/admin/templates') | ForEach-Object -Process { $_.agency } }
        'AgencyProfile' { @(Invoke-PaApi @call -Path '/api/v1/admin/agency-profiles') }
        'Category' { @((Invoke-PaApi @call -Path '/api/v1/meta').categories) }
    }
    # Distinct, ignoring case, in the order the server gave them.
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($value in $values) {
        if (-not [string]::IsNullOrEmpty($value) -and $seen.Add([string]$value)) {
            [string]$value
        }
    }
}

function ConvertTo-PaCompletionText {
    <#
    .SYNOPSIS
    A value as it's typed on the command line, quoted when it needs to be.

    .DESCRIPTION
    A value with a space or a character PowerShell would read as syntax is
    put in single quotes, with any single quote in it doubled. When the
    user has started typing a quote, the same quote is used.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # The value.
        [Parameter(Mandatory)]
        [string] $Value,

        # The quote the user started with, if any.
        [string] $Quote = ''
    )
    # Regular expressions, with the typographic quotes PowerShell also
    # reads as quotes written as \u escapes.
    $doubleQuote = '^["\u201C-\u201E]$'
    $special = '[\s$`''"\u2018-\u201E;,(){}@#&|<>\[\]*?]'
    if ($Quote -match $doubleQuote) {
        $escaped = [regex]::Replace($Value, '[`$"\u201C-\u201E]', '`$0')
        return '"' + $escaped + '"'
    }
    if ($Quote -or $Value -match $special -or $Value -match '^[-+.0-9]') {
        $escaped = [regex]::Replace($Value, '[''\u2018-\u201B]', '$0$0')
        return "'" + $escaped + "'"
    }
    $Value
}

function Get-PaCompletion {
    <#
    .SYNOPSIS
    Completes a parameter from one of the small lists (R63, owner 2026-11-19).

    .DESCRIPTION
    Each list is cached for this PowerShell session, for 5 minutes, then
    fetched again. When what was typed matches nothing in the cache, the
    server is asked once more, so a template added a moment ago shows up.
    Not connected in this session, or the server can't be reached: nothing
    is offered, and nothing is shown. Values with spaces are quoted.

    Only real values are offered. For a parameter that takes wildcards,
    typed wildcards narrow the values offered (Ex*Fire offers Example Fire)
    and the typed pattern itself still works when run; otherwise what's
    typed is matched as its own text.
    #>
    [CmdletBinding()]
    [OutputType([System.Management.Automation.CompletionResult])]
    param(
        # Template, Agency, AgencyProfile or Category.
        [Parameter(Mandatory)]
        [ValidateSet('Template', 'Agency', 'AgencyProfile', 'Category')]
        [string] $List,

        # What the user has typed so far.
        [AllowEmptyString()]
        [AllowNull()]
        [string] $WordToComplete = '',

        # The parameter takes wildcards.
        [switch] $Wildcard
    )
    # Never a message while the user is typing.
    $ErrorActionPreference = 'Stop'
    $WarningPreference = 'SilentlyContinue'
    $InformationPreference = 'SilentlyContinue'
    $ProgressPreference = 'SilentlyContinue'
    try {
        # Only this session's connection: completing never opens a saved
        # sign-in, which could need the network or a vault's password.
        $connection = $script:Connection
        if (-not $connection) {
            return
        }
        $quote = ''
        $typed = [string]$WordToComplete
        if ($typed.Length -and "'`"`u{2018}`u{2019}`u{201A}`u{201B}`u{201C}`u{201D}`u{201E}".Contains($typed[0])) {
            $quote = [string]$typed[0]
            $typed = $typed.Substring(1)
            if ($typed.Length -and $typed[-1] -eq $quote[0]) {
                $typed = $typed.Substring(0, $typed.Length - 1)
            }
        }
        $pattern = if ($Wildcard -and [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($typed)) { "$typed*" } else { [System.Management.Automation.WildcardPattern]::Escape($typed) + '*' }
        $match = [System.Management.Automation.WildcardPattern]::new($pattern, [System.Management.Automation.WildcardOptions]::IgnoreCase)

        $entry = $script:CompletionCache[$List]
        $fresh = $entry -and $entry.Origin -eq $connection.Uri -and ([datetimeoffset]::UtcNow - $entry.FetchedAt) -lt $script:CompletionLifetime
        $fetched = $false
        if (-not $fresh) {
            $entry = @{ Values = @(Get-PaCompletionValue -List $List); FetchedAt = [datetimeoffset]::UtcNow; Origin = $connection.Uri }
            $script:CompletionCache[$List] = $entry
            $fetched = $true
        }
        $found = @($entry.Values | Where-Object -FilterScript { $match.IsMatch($_) })
        if (-not $found.Count -and -not $fetched) {
            # Nothing matches: it may be new since the list was fetched.
            $entry = @{ Values = @(Get-PaCompletionValue -List $List); FetchedAt = [datetimeoffset]::UtcNow; Origin = $connection.Uri }
            $script:CompletionCache[$List] = $entry
            $found = @($entry.Values | Where-Object -FilterScript { $match.IsMatch($_) })
        }
        foreach ($value in ($found | Sort-Object -Culture ([cultureinfo]::InvariantCulture))) {
            [System.Management.Automation.CompletionResult]::new((ConvertTo-PaCompletionText -Value $value -Quote $quote), $value, 'ParameterValue', $value)
        }
    } catch {
        # Not signed in any more, or the server can't be reached: nothing.
        $null = $_
    }
}

function Clear-PaCompletionCache {
    <#
    .SYNOPSIS
    Forgets the cached lists (on signing out).
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Clears an in-memory cache; changes nothing on the server.')]
    [CmdletBinding()]
    param()
    $script:CompletionCache = @{}
}

function Get-PaParameterCompletion {
    <#
    .SYNOPSIS
    Completes one command's parameter (called by PagerArchiveCompleter).

    .DESCRIPTION
    The list comes from the parameter's name: -Template, and -Name on the
    template commands, complete template names; -Agency, -AgencyProfile
    and -Category complete their own lists. Typed wildcards narrow the
    values offered only when the parameter takes wildcards
    ([SupportsWildcards()]).
    #>
    [CmdletBinding()]
    [OutputType([System.Management.Automation.CompletionResult])]
    param(
        # The command, as typed (it may be a Pa alias).
        [Parameter(Mandatory)]
        [string] $CommandName,

        # The parameter being completed.
        [Parameter(Mandatory)]
        [string] $ParameterName,

        # What the user has typed so far.
        [AllowEmptyString()]
        [AllowNull()]
        [string] $WordToComplete = ''
    )
    $list = if ($ParameterName -eq 'Name') { 'Template' } else { $ParameterName }
    if ($list -notin @('Template', 'Agency', 'AgencyProfile', 'Category')) {
        return
    }
    $command = Get-Command -Name $CommandName -ErrorAction SilentlyContinue | Select-Object -First 1
    $parameter = if ($command) { $command.Parameters[$ParameterName] }
    $wildcard = [bool]($parameter -and ($parameter.Attributes | Where-Object -FilterScript { $_ -is [System.Management.Automation.SupportsWildcardsAttribute] }))
    Get-PaCompletion -List $list -WordToComplete $WordToComplete -Wildcard:$wildcard
}
