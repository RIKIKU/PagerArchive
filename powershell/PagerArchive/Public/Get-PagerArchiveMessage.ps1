function Get-PagerArchiveMessage {
    <#
    .SYNOPSIS
    Searches messages, newest first.

    .DESCRIPTION
    Returns messages with what the detail view shows (R63a): the full text,
    priority, sent time, capcode, alias, agency, group, job number, event
    category, parsed fields, location, flags and each client's receive time.

    Filters combine (R33): a message must match every filter given, and a
    filter given several values matches any of them. Pages to ignored
    aliases are left out unless -IncludeIgnored is given (R27). With no
    filter it returns every message.

    Messages are fetched a page at a time, only as the pipeline asks for
    more, so -First 10 or Select-Object -First 10 stops after the first page.
    -Skip leaves out the newest matches first. There's no
    -IncludeTotalCount: count with Measure-Object or @(...).Count.
    Aliases and capcodes piped in are combined into one search.

    .EXAMPLE
    Get-PagerArchiveMessage -Capcode 0100001 -Priority Emergency

    SentAt               Priority  Alias     Text
    ------               --------  -----     ----
    20/10/2026 9:14:03 am Emergency Station 1 EX0000031 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #TV1

    The table shows SentAt in your time zone; the object's SentAt is the
    [datetimeoffset] the server sent.

    .EXAMPLE
    Get-PagerArchiveMessage -First 1 | Format-List

    Shows what the browser's detail view shows: the full text, the parsed
    fields, the location, the flags and each client's receive time, with
    times in your time zone.

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Station*' | Get-PagerArchiveMessage -After (Get-Date).AddDays(-7)

    .EXAMPLE
    Get-PagerArchiveMessage -Text 'SHED FIRE' -First 20 | Export-Csv -Path fires.csv

    .EXAMPLE
    '0100001', '0100002' | ForEach-Object -Process { [pscustomobject]@{ Capcode = $_ } } | Get-PagerArchiveMessage -First 10

    Capcodes piped in as objects with a Capcode property.

    .EXAMPLE
    Get-PagerArchiveMessage -Unaliased -After (Get-Date).AddDays(-1) | Group-Object -Property Capcode

    Capcodes with no alias that paged in the last day.

    .EXAMPLE
    Get-PagerArchiveMessage -Capcode 0100001 -Skip 20 -First 20

    The second 20 messages to one capcode, newest first.

    .EXAMPLE
    (Get-PagerArchiveMessage -JobNumber EX0000031 | Measure-Object).Count

    How many messages a job has.

    .INPUTS
    System.Management.Automation.PSObject. Objects with a Capcode property
    (such as PagerArchive.Message), or with a Name property (such as
    PagerArchive.Alias), whose value is taken as an alias name.

    .OUTPUTS
    PagerArchive.Message. Id, SentAt, Capcode, Alias, Agency, Priority
    (Emergency, Non-emergency, Admin or None), Text (as a pager shows it,
    in full), RawText (as received, with the priority marker), Group,
    JobNumber, EventType, Category, Description, Address, Locality, GridRef,
    Units, Fields (other parsed fields), Location (Longitude, Latitude,
    Approximate, Source), ParseFailed, AddressUnsplit, LocationMismatch,
    Heartbeat, Ignored and Receptions (Client, ReceivedAt).

    .LINK
    Get-PagerArchiveAlias

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Message')]
    param(
        # Text the message, or one of its parsed fields, contains
        # (case-insensitive). Default: any. No wildcards.
        [Parameter(Position = 0)]
        [ValidateNotNullOrEmpty()]
        [string] $Text,

        # Capcodes (1 to 7 digits). Default: any. No wildcards.
        [Parameter(ValueFromPipelineByPropertyName)]
        [ValidatePattern('^[0-9]{1,7}$')]
        [string[]] $Capcode,

        # Alias names. Accepts wildcards, matched case-insensitively; a name
        # without wildcards matches exactly. Default: any.
        [Parameter(ValueFromPipelineByPropertyName)]
        [Alias('Name')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Alias,

        # Agency names. Accepts wildcards, matched case-insensitively.
        # Default: any.
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Agency,

        # Paging groups. Accepts wildcards, matched case-insensitively.
        # Default: any.
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Group,

        # Priorities. Default: any.
        [ValidateSet('Emergency', 'Non-emergency', 'Admin', 'None')]
        [string[]] $Priority,

        # Event categories, as the network profile names them, or Other.
        # Default: any. No wildcards.
        [ArgumentCompleter({
                param($commandName, $parameterName, $wordToComplete)
                $null = $commandName, $parameterName
                try {
                    (Invoke-PaApi -Method GET -Path '/api/v1/meta').categories |
                        Where-Object -FilterScript { $_ -like "$wordToComplete*" } |
                        ForEach-Object -Process { [System.Management.Automation.CompletionResult]::new("'$_'", $_, 'ParameterValue', $_) }
                } catch {
                    $null = $_
                }
            })]
        [ValidateNotNullOrEmpty()]
        [string[]] $Category,

        # Job numbers. Default: any. No wildcards.
        [ValidateNotNullOrEmpty()]
        [string[]] $JobNumber,

        # Only messages sent at or after this time. Default: no limit.
        [datetimeoffset] $After,

        # Only messages sent before this time. Default: no limit.
        [datetimeoffset] $Before,

        # Only messages to capcodes with no alias. Off by default.
        [switch] $Unaliased,

        # Include pages to ignored aliases. Off by default.
        [switch] $IncludeIgnored,

        # Only messages with any of these flags. Default: any.
        [ValidateSet('ParseFailed', 'AddressUnsplit', 'LocationMismatch')]
        [string[]] $Flag,

        # Returns only this many messages, fetching no more pages than that
        # needs. Default: every message.
        [uint64] $First,

        # Leaves out this many matching messages, newest first, before
        # returning any. Default: 0.
        [uint64] $Skip
    )
    begin {
        $capcodes = [System.Collections.Generic.List[string]]::new()
        $aliases = [System.Collections.Generic.List[string]]::new()
    }
    process {
        foreach ($c in $Capcode) { $capcodes.Add($c) }
        foreach ($a in $Alias) { $aliases.Add($a) }
    }
    end {
        # Every piped alias and capcode, in one search (A55).
        $query = [ordered]@{
            q           = $Text
            capcode     = @($capcodes)
            alias_like  = @()
            agency_like = @()
            group_like  = @()
            priority    = @(foreach ($p in $Priority) { if ($p -eq 'None') { 'none' } else { $p } })
            category    = @($Category)
            job         = @($JobNumber)
            from        = if ($PSBoundParameters.ContainsKey('After')) { $After.ToString('o') } else { $null }
            to          = if ($PSBoundParameters.ContainsKey('Before')) { $Before.ToString('o') } else { $null }
            unaliased   = if ($Unaliased) { 'true' } else { $null }
            ignored     = if ($IncludeIgnored) { 'true' } else { $null }
            flag        = @(foreach ($f in $Flag) { @{ ParseFailed = 'parse_failed'; AddressUnsplit = 'address_unsplit'; LocationMismatch = 'location_mismatch' }[$f] })
            receptions  = 'true'
            limit       = 200
            before      = $null
        }
        # Wildcards become LIKE patterns; a [set], which LIKE can't express,
        # is checked here as well.
        $check = @{}
        foreach ($pair in @(@('alias_like', @($aliases), 'Alias'), @('agency_like', @($Agency), 'Agency'), @('group_like', @($Group), 'Group'))) {
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

        # Our own -First and -Skip, not SupportsPaging, so there's no
        # -IncludeTotalCount (guidelines section 3).
        $first = if ($PSBoundParameters.ContainsKey('First')) { $First } else { [uint64]::MaxValue }
        $skip = $Skip
        $written = [uint64]0
        $seen = [uint64]0
        try {
            while ($written -lt $first) {
                $wanted = ([double]$first - $written) + [math]::Max(0, [double]$skip - $seen)
                $query.limit = [int][math]::Min(200.0, [math]::Max(1.0, $wanted))
                $page = Invoke-PaApi -Method GET -Path '/api/v1/messages' -Query $query
                foreach ($row in $page.messages) {
                    $message = ConvertTo-PaMessage -Row $row
                    $match = $true
                    foreach ($property in $check.Keys) {
                        if (-not ($check[$property] | Where-Object -FilterScript { $_.IsMatch([string]$message.$property) })) {
                            $match = $false
                        }
                    }
                    if (-not $match) { continue }
                    $seen++
                    if ($seen -le $skip) { continue }
                    $message
                    $written++
                    if ($written -ge $first) { break }
                }
                if (-not $page.PSObject.Properties['next'] -or -not $page.next) {
                    break
                }
                $query.before = $page.next
            }
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
        }
    }
}
