function Watch-PagerArchiveMessage {
    <#
    .SYNOPSIS
    Outputs new messages as they arrive, until stopped with Ctrl+C.

    .DESCRIPTION
    Watches PagerArchive's live updates and writes each new message that
    matches the filters down the pipeline the moment it arrives, in arrival
    order, as the same PagerArchive.Message objects Get-PagerArchiveMessage
    returns. It runs until you press Ctrl+C (R63b).

    The filters are Get-PagerArchiveMessage's, except for time ranges and
    paging: a message must match every filter given, and a filter given
    several values matches any of them. Pages to ignored aliases are left
    out unless -IncludeIgnored is given. Aliases and capcodes piped in are
    combined into one watch.

    If the connection is lost, it warns once and keeps retrying, waiting 1,
    2, 4, 8, 16 and then 30 seconds between tries (-Verbose shows each). When
    it's back, a second warning says how long it was down and how many
    messages follow; then it outputs the matching messages that arrived
    meanwhile, oldest first, and carries on. Each message comes out once.

    It uses the system's proxy settings, as every other command does.

    It renews your sign-in as it goes, quietly, so it can run for days. It
    stops with an error if your account is deactivated, you're no longer an
    admin, or your sign-in has ended (for example, revoked in Authentik).

    Like integrations, it's a convenience, not an alerting system: don't use
    it in place of an issued pager.

    .EXAMPLE
    Watch-PagerArchiveMessage -Capcode 0100001

    SentAt                Priority  Alias     Text
    ------                --------  -----     ----
    22/10/2026 4:12:09 pm Emergency Station 1 EX0000041 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1

    Each new message to capcode 0100001 as it arrives, until Ctrl+C.

    .EXAMPLE
    Watch-PaMessage -Priority Emergency | ForEach-Object -Process { "$($_.Alias): $($_.Text)" }

    Reacts to each Emergency page the moment it arrives.

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Station*' | Watch-PagerArchiveMessage

    Watches every alias whose name starts with Station.

    .EXAMPLE
    Watch-PagerArchiveMessage -Category 'Structure fire' -Verbose

    Also shows each retry while the connection is lost.

    .EXAMPLE
    Watch-PagerArchiveMessage -JobNumber EX0000041 | Export-Csv -Path job.csv -Append

    Adds each new message about one job to a CSV file as it arrives.

    .INPUTS
    System.Management.Automation.PSObject. Objects with a Capcode property
    (such as PagerArchive.Message), or with a Name property (such as
    PagerArchive.Alias), whose value is taken as an alias name.

    .OUTPUTS
    PagerArchive.Message. The same as Get-PagerArchiveMessage's: Id, SentAt,
    Capcode, Alias, Agency, Priority, Text (as a pager shows it, in full),
    RawText, Group, JobNumber, EventType, Category, Description, Address,
    Locality, GridRef, Units, Fields, Location, ParseFailed, AddressUnsplit,
    LocationMismatch, Heartbeat, Ignored and Receptions. Receptions holds the
    receive times known when the message arrived, usually the first
    client's; Get-PagerArchiveMessage shows any later ones.

    .LINK
    Get-PagerArchiveMessage

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

        # Only messages to capcodes with no alias. Off by default.
        [switch] $Unaliased,

        # Include pages to ignored aliases. Off by default.
        [switch] $IncludeIgnored,

        # Only messages with any of these flags. Default: any.
        [ValidateSet('ParseFailed', 'AddressUnsplit', 'LocationMismatch')]
        [string[]] $Flag
    )
    begin {
        $capcodes = [System.Collections.Generic.List[string]]::new()
        $aliases = [System.Collections.Generic.List[string]]::new()
        $stream = $null
    }
    process {
        foreach ($c in $Capcode) { $capcodes.Add($c) }
        foreach ($a in $Alias) { $aliases.Add($a) }
    }
    end {
        # Every piped alias and capcode, in one watch.
        $filter = @{
            Text           = $Text
            Capcode        = @($capcodes)
            Alias          = @($aliases)
            Agency         = $Agency
            Group          = $Group
            Priority       = $Priority
            Category       = $Category
            JobNumber      = $JobNumber
            Unaliased      = $Unaliased
            IncludeIgnored = $IncludeIgnored
            Flag           = $Flag
        }
        $filter = New-PaMessageFilter @filter
        $query = $filter.Query
        $query.receptions = 'true'
        $query.after_id = $null

        $last = $null        # the next after_id: the newest message accounted for
        $connection = $null
        $connected = $false  # a stream has been ready at least once
        $lost = $null        # how long the connection has been lost, while it is
        $attempt = 0         # tries since it was lost
        $renewed = $false    # renewed once after invalid_token, for this try
        while ($true) {
            $failure = $null
            try {
                $connection = Get-PaConnection
                if ($connection.ExpiresAt -lt [datetimeoffset]::UtcNow.AddSeconds(60)) {
                    Request-PaToken -Connection $connection
                }
                $query.after_id = $last
                $names = @($query.Keys | Where-Object -FilterScript { $null -ne $query[$_] -and "$($query[$_])" -ne '' })
                Write-Verbose -Message "GET /api/v1/stream ($($names -join ', '))"
                $stream = Open-PaEventStream -Connection $connection -PathAndQuery ('/api/v1/stream' + (ConvertTo-PaQueryString -Query $query))
                Write-Debug -Message "GET /api/v1/stream answered $($stream.Status)"
                if ($stream.Status -eq 401 -and $stream.Code -eq 'invalid_token' -and -not $renewed) {
                    # A token Authentik has since refused, or one that expired
                    # early: renew once and try again.
                    $connection.ExpiresAt = [datetimeoffset]::MinValue
                    $renewed = $true
                    continue
                }
                if ($stream.Status -ne 200) {
                    $code = if ($stream.Code) { $stream.Code } else { "http_$($stream.Status)" }
                    $message = if ($stream.Message) { $stream.Message } else { "PagerArchive answered $($stream.Status)." }
                    throw (New-PaErrorRecord -ErrorId $code -Status $stream.Status -Message $message)
                }
            } catch {
                $failure = $_
            }
            $renewed = $false

            if (-not $failure) {
                # Read events until the stream ends.
                try {
                    while ($true) {
                        $next = Read-PaStreamEvent -State $stream -SilenceSec $script:WatchSilenceSec
                        Write-Debug -Message "event $($next.Type) $($next.Id)"
                        if ($next.Type -eq 'message') {
                            $row = $next.Data | ConvertFrom-Json
                            if ($null -ne $last -and [long]$row.id -le $last) {
                                Write-Debug -Message "message $($row.id) skipped: already output"
                                continue
                            }
                            $last = [long]$row.id
                            $message = ConvertTo-PaMessage -Row $row
                            if (Test-PaMessageFilter -Message $message -Check $filter.Check) {
                                $message
                            }
                        } elseif ($next.Type -eq 'catch-up') {
                            # Before the messages that arrived meanwhile: how many follow.
                            $count = [long]($next.Data | ConvertFrom-Json).count
                            if ($null -ne $lost) {
                                $follow = switch ($count) {
                                    0 { 'no messages arrived meanwhile' }
                                    1 { '1 message that arrived meanwhile follows' }
                                    default { '{0:N0} messages that arrived meanwhile follow' -f $count }
                                }
                                Write-Warning -Message "Reconnected after $(Format-PaDuration -Duration $lost.Elapsed); $follow."
                                $lost = $null
                            } elseif ($count -gt 1000) {
                                # A quiet reconnect (the server's), with a lot to catch up.
                                Write-Warning -Message ('{0:N0} messages arrived while reconnecting; they follow, oldest first.' -f $count)
                            }
                        } elseif ($next.Type -eq 'ready') {
                            $ready = $next.Data | ConvertFrom-Json
                            if ($null -eq $last -or [long]$ready.last_id -gt $last) {
                                $last = [long]$ready.last_id
                            }
                            $connected = $true
                            $attempt = 0
                            if ($null -ne $lost) {
                                # A server without the catch-up event.
                                Write-Warning -Message "Reconnected after $(Format-PaDuration -Duration $lost.Elapsed)."
                                $lost = $null
                            }
                            if ($null -ne $query.after_id) {
                                Write-Verbose -Message "Caught up: $($ready.caught_up) messages arrived while reconnecting."
                            }
                        } elseif ($next.Type -eq 'reconnect') {
                            # The server ended the stream on purpose (the token's
                            # expiry, an account change, or falling behind):
                            # reconnect at once, quietly; the catch-up fills the gap.
                            Write-Debug -Message "reconnect: $(($next.Data | ConvertFrom-Json).reason)"
                            break
                        }
                    }
                } catch {
                    $failure = $_
                } finally {
                    Close-PaEventStream -State $stream
                    $stream = $null
                }
                if (-not $failure) {
                    continue
                }
            }

            # A refusal, or no watch yet: stop. A lost connection: retry.
            $code = $failure.Exception.Data['PagerArchive.Code']
            $retry = $connected -and ($failure.CategoryInfo.Category -in @('ConnectionError', 'LimitsExceeded') -or $code -eq 'renewal_failed')
            if (-not $retry) {
                $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $failure))
            }
            if ($null -eq $lost) {
                $lost = [System.Diagnostics.Stopwatch]::StartNew()
                $reason = $failure.Exception.Message -replace '^PagerArchive can''t be reached at [^ ]+: ', ''
                Write-Warning -Message "Lost the connection to PagerArchive at $($connection.Uri) ($reason). Retrying until it's back; messages that arrive meanwhile will follow."
            }
            $attempt++
            $delay = [int][math]::Min(30, [math]::Pow(2, $attempt - 1))
            Write-Verbose -Message "Reconnecting to PagerArchive in $delay s (attempt $attempt)."
            Wait-PaRetry -Seconds $delay
        }
    }
    clean {
        # Whichever way it ends (Ctrl+C, an error, or a later command such as
        # Select-Object -First stopping it), the connection is closed.
        Close-PaEventStream -State $stream
    }
}
