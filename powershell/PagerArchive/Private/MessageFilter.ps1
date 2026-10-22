# The message filters Get-PagerArchiveMessage and Watch-PagerArchiveMessage
# share (R63a, R63b): the same parameters make the same query.

function New-PaMessageFilter {
    <#
    .SYNOPSIS
    Turns the message commands' filters into the API's query parameters.

    .DESCRIPTION
    Returns Query, an ordered dictionary of query parameters (arrays repeat
    the name), and Check: for wildcards LIKE can't express, such as [a-c],
    the property to check on each message and the patterns it must match one
    of (Test-PaMessageFilter).
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Makes an object in memory; changes nothing.')]
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # Text the message or a parsed field contains.
        [string] $Text,

        # Capcodes.
        [string[]] $Capcode,

        # Alias names, with wildcards.
        [string[]] $Alias,

        # Agency names, with wildcards.
        [string[]] $Agency,

        # Paging groups, with wildcards.
        [string[]] $Group,

        # Priorities: Emergency, Non-emergency, Admin or None.
        [string[]] $Priority,

        # Event categories.
        [string[]] $Category,

        # Job numbers.
        [string[]] $JobNumber,

        # Only capcodes with no alias.
        [switch] $Unaliased,

        # Include ignored aliases.
        [switch] $IncludeIgnored,

        # Flags: ParseFailed, AddressUnsplit, LocationMismatch.
        [string[]] $Flag
    )
    $query = [ordered]@{
        q           = $Text
        capcode     = @($Capcode)
        alias_like  = @()
        agency_like = @()
        group_like  = @()
        priority    = @(foreach ($p in $Priority) { if ($p -eq 'None') { 'none' } else { $p } })
        category    = @($Category)
        job         = @($JobNumber)
        unaliased   = if ($Unaliased) { 'true' } else { $null }
        ignored     = if ($IncludeIgnored) { 'true' } else { $null }
        flag        = @(foreach ($f in $Flag) { @{ ParseFailed = 'parse_failed'; AddressUnsplit = 'address_unsplit'; LocationMismatch = 'location_mismatch' }[$f] })
    }
    # Wildcards become LIKE patterns; a [set], which LIKE can't express, is
    # checked on each message as well.
    $check = @{}
    foreach ($pair in @(@('alias_like', @($Alias), 'Alias'), @('agency_like', @($Agency), 'Agency'), @('group_like', @($Group), 'Group'))) {
        $likes = foreach ($w in $pair[1]) {
            if (-not $w) { continue }
            $converted = ConvertTo-PaLikePattern -Wildcard $w
            if (-not $converted.Exact) {
                $check[$pair[2]] = @($pair[1] | ForEach-Object -Process { [System.Management.Automation.WildcardPattern]::new($_, [System.Management.Automation.WildcardOptions]::IgnoreCase) })
            }
            $converted.Like
        }
        $query[$pair[0]] = @($likes)
    }
    @{ Query = $query; Check = $check }
}

function Test-PaMessageFilter {
    <#
    .SYNOPSIS
    Whether a message matches the wildcards the API couldn't check (New-PaMessageFilter's Check).
    #>
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        # The message.
        [Parameter(Mandatory)]
        [psobject] $Message,

        # Property name -> patterns, one of which must match.
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [hashtable] $Check
    )
    foreach ($property in $Check.Keys) {
        if (-not ($Check[$property] | Where-Object -FilterScript { $_.IsMatch([string]$Message.$property) })) {
            return $false
        }
    }
    $true
}
