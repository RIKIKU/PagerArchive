# The live stream (GET /api/v1/stream) for Watch-PagerArchiveMessage (ADR
# 0010 section 6). Invoke-RestMethod reads a whole response before it
# returns, so the stream is read with .NET's HttpClient as it arrives. That
# is guidelines section 9's one exception: the access token becomes text
# only inside Add-PaBearerHeader, as Invoke-RestMethod does internally.
# HttpClient follows the system's proxy settings and checks certificates as
# Invoke-RestMethod does. Every wait is a slice of 200 ms, so Ctrl+C can stop
# the command between slices.

function Wait-PaTask {
    <#
    .SYNOPSIS
    Waits for a task in slices of 200 ms, so Ctrl+C can stop the wait.

    .DESCRIPTION
    Returns $true when the task finished, or $false when TimeoutMs passed
    first (it's still running). A failed task throws its own error.
    #>
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        # The task.
        [Parameter(Mandatory)]
        [System.Threading.Tasks.Task] $Task,

        # How long to wait, in milliseconds; -1 waits for as long as it takes.
        [int] $TimeoutMs = -1
    )
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    while (-not $Task.IsCompleted) {
        if ($TimeoutMs -ge 0 -and $watch.ElapsedMilliseconds -ge $TimeoutMs) {
            return $false
        }
        $null = [System.Threading.Tasks.Task]::WhenAny($Task).Wait(200)
    }
    if ($Task.IsFaulted) {
        throw $Task.Exception.GetBaseException()
    }
    if ($Task.IsCanceled) {
        throw [System.OperationCanceledException]::new()
    }
    $true
}

function Add-PaBearerHeader {
    <#
    .SYNOPSIS
    Sets a request's Authorization header to the access token.

    .DESCRIPTION
    The only place the module turns an access token into text (guidelines
    section 9): straight into the header, as Invoke-RestMethod
    -Authentication Bearer does, never into a variable. The caller clears
    the header once the request is sent.
    #>
    [CmdletBinding()]
    param(
        # The request.
        [Parameter(Mandatory)]
        [System.Net.Http.HttpRequestMessage] $Request,

        # The access token.
        [Parameter(Mandatory)]
        [securestring] $Token
    )
    $Request.Headers.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', [System.Net.NetworkCredential]::new('', $Token).Password)
}

function Open-PaEventStream {
    <#
    .SYNOPSIS
    Opens the live stream as the signed-in admin and reads the reply's status.

    .DESCRIPTION
    Returns the stream's state, for Read-PaStreamEvent and
    Close-PaEventStream. Status is the HTTP status; for anything but 200, the
    body is read too, and Code and Message hold the API's refusal. Failing
    to connect throws a ConnectionError. A plain http:// URL is refused
    unless the sign-in allowed it (-AllowUnencryptedAuthentication), as
    Invoke-RestMethod refuses it for every other command.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The connection, whose access token is sent.
        [Parameter(Mandatory)]
        [hashtable] $Connection,

        # The path and query string, already escaped.
        [Parameter(Mandatory)]
        [string] $PathAndQuery,

        # How long connecting and the reply's headers may take.
        [int] $TimeoutSec = 30
    )
    $uri = [uri]($Connection.Uri + $PathAndQuery)
    $origin = Get-PaOrigin -Uri $uri -AllowUnencryptedAuthentication:([bool]$Connection.AllowUnencrypted)
    $state = @{
        Client   = $null
        Cancel   = [System.Threading.CancellationTokenSource]::new()
        Response = $null
        Stream   = $null
        Pending  = $null
        Buffer   = [byte[]]::new(16384)
        Body     = [System.Collections.Generic.List[byte]]::new()
        Closed   = $false
        LastData = [System.Diagnostics.Stopwatch]::StartNew()
        Event    = @{ Type = ''; Id = ''; Data = [System.Collections.Generic.List[string]]::new() }
        Status   = 0
        Code     = $null
        Message  = $null
    }
    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Get, $uri)
    $opened = $false
    try {
        try {
            # A new client for each connection: closing it closes the socket.
            # Its proxy and certificate checks are .NET's defaults, as
            # Invoke-RestMethod's are.
            $state.Client = [System.Net.Http.HttpClient]::new()
            $state.Client.Timeout = [System.Threading.Timeout]::InfiniteTimeSpan
            $request.Headers.Accept.ParseAdd('text/event-stream')
            $request.Headers.CacheControl = [System.Net.Http.Headers.CacheControlHeaderValue]@{ NoCache = $true }
            $request.Headers.UserAgent.ParseAdd('PagerArchive-PowerShell')
            Add-PaBearerHeader -Request $request -Token $Connection.AccessToken
            $send = $state.Client.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $state.Cancel.Token)
            if (-not (Wait-PaTask -Task $send -TimeoutMs ($TimeoutSec * 1000))) {
                throw [System.TimeoutException]::new("no answer within $TimeoutSec s")
            }
            $state.Response = $send.Result
            $state.Status = [int]$state.Response.StatusCode
            if ($state.Status -eq 200) {
                $read = $state.Response.Content.ReadAsStreamAsync($state.Cancel.Token)
                $null = Wait-PaTask -Task $read
                $state.Stream = $read.Result
            }
        } catch {
            throw (New-PaErrorRecord -ErrorId 'server_unreachable' -Category ConnectionError -Message "PagerArchive can't be reached at ${origin}: $($_.Exception.GetBaseException().Message)")
        }
        if ($state.Status -ne 200) {
            # A refusal: read its JSON for the API's code and message.
            try {
                $read = $state.Response.Content.ReadAsByteArrayAsync($state.Cancel.Token)
                if (Wait-PaTask -Task $read -TimeoutMs ($TimeoutSec * 1000)) {
                    $text = [System.Text.Encoding]::UTF8.GetString($read.Result)
                    if ($text -and $text.TrimStart().StartsWith('{')) {
                        $reply = $text | ConvertFrom-Json -AsHashtable
                        $state.Code = $reply['code']
                        $state.Message = $reply['error']
                    }
                }
            } catch {
                $null = $_
            }
            Close-PaEventStream -State $state
        }
        $opened = $true
    } finally {
        $request.Headers.Authorization = $null
        $request.Dispose()
        # An error, or Ctrl+C while connecting: nothing is left open.
        if (-not $opened) {
            Close-PaEventStream -State $state
        }
    }
    $state
}

function Receive-PaStreamData {
    <#
    .SYNOPSIS
    Waits up to 200 ms for more of the reply's body.
    #>
    [CmdletBinding()]
    param(
        # The stream's state.
        [Parameter(Mandatory)]
        [hashtable] $State
    )
    if ($State.Closed) {
        return
    }
    if ($null -eq $State.Pending) {
        $State.Pending = $State.Stream.ReadAsync($State.Buffer, 0, $State.Buffer.Length)
    }
    if (Wait-PaTask -Task $State.Pending -TimeoutMs 200) {
        $n = $State.Pending.Result
        $State.Pending = $null
        if ($n -eq 0) {
            $State.Closed = $true
        } else {
            $State.Body.AddRange([System.ArraySegment[byte]]::new($State.Buffer, 0, $n))
            $State.LastData.Restart()
        }
    }
}

function Read-PaStreamEvent {
    <#
    .SYNOPSIS
    Reads the stream's next event.

    .DESCRIPTION
    Returns a hashtable: Type (message, catch-up, ready, reconnect, ...), Id
    and Data. Comments (the server's keepalives) are read and skipped.
    Throws a ConnectionError (connection_lost) when the stream ends, a read
    fails, or nothing at all arrives for SilenceSec: the server sends a
    keepalive every 25 seconds, so silence means a connection that died
    without closing.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The stream's state (Open-PaEventStream).
        [Parameter(Mandatory)]
        [hashtable] $State,

        # How long without a byte counts as a lost connection.
        [double] $SilenceSec = 60
    )
    $current = $State.Event
    while ($true) {
        $i = $State.Body.IndexOf([byte]10)
        if ($i -ge 0) {
            $line = [System.Text.Encoding]::UTF8.GetString($State.Body.GetRange(0, $i).ToArray()).TrimEnd("`r")
            $State.Body.RemoveRange(0, $i + 1)
            if ($line -eq '') {
                if ($current.Data.Count -gt 0 -or $current.Type) {
                    $out = @{ Type = if ($current.Type) { $current.Type } else { 'message' }; Id = $current.Id; Data = $current.Data -join "`n" }
                    $current.Type = ''
                    $current.Id = ''
                    $current.Data.Clear()
                    return $out
                }
                continue
            }
            if ($line.StartsWith(':')) {
                continue
            }
            $field, $value = $line -split ':', 2
            if ($null -ne $value -and $value.StartsWith(' ')) {
                $value = $value.Substring(1)
            }
            switch ($field) {
                'event' { $current.Type = $value }
                'id' { $current.Id = $value }
                'data' { $current.Data.Add([string]$value) }
            }
            continue
        }
        $reason = $null
        if ($State.Closed) {
            $reason = 'the server closed the connection'
        } elseif ($State.LastData.Elapsed.TotalSeconds -ge $SilenceSec) {
            $reason = "nothing arrived for $([int]$SilenceSec) s"
        } else {
            try {
                Receive-PaStreamData -State $State
            } catch {
                # A chunked reply cut off without its last chunk is how a
                # dropped connection usually ends.
                $base = $_.Exception.GetBaseException()
                $reason = if ($base -is [System.Net.Http.HttpIOException] -and $base.HttpRequestError -eq 'ResponseEnded') { 'the server closed the connection' } else { $base.Message }
            }
        }
        if ($reason) {
            throw (New-PaErrorRecord -ErrorId 'connection_lost' -Category ConnectionError -Message $reason)
        }
    }
}

function Close-PaEventStream {
    <#
    .SYNOPSIS
    Closes the stream and its connection. Safe to call more than once.
    #>
    [CmdletBinding()]
    param(
        # The stream's state.
        [Parameter(Mandatory)]
        [AllowNull()]
        [hashtable] $State
    )
    if ($null -eq $State) {
        return
    }
    if ($null -ne $State.Cancel) {
        try {
            $State.Cancel.Cancel()
        } catch {
            $null = $_
        }
    }
    foreach ($thing in @($State.Stream, $State.Response, $State.Client, $State.Cancel)) {
        if ($null -ne $thing) {
            try {
                $thing.Dispose()
            } catch {
                $null = $_
            }
        }
    }
    $State.Stream = $null
    $State.Response = $null
    $State.Client = $null
    $State.Cancel = $null
    $State.Pending = $null
    $State.Closed = $true
}

# How long without a byte counts as a lost connection (the server sends a
# keepalive every 25 seconds). Tests lower it.
$script:WatchSilenceSec = 60

function Wait-PaRetry {
    <#
    .SYNOPSIS
    Waits before trying to reconnect. Ctrl+C stops the wait.
    #>
    [CmdletBinding()]
    param(
        # How long, in seconds.
        [Parameter(Mandatory)]
        [int] $Seconds
    )
    Start-Sleep -Seconds $Seconds
}

function Format-PaDuration {
    <#
    .SYNOPSIS
    A duration as people say it: "45 s", "3 min 12 s", "2 h 5 min".
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # The duration.
        [Parameter(Mandatory)]
        [timespan] $Duration
    )
    if ($Duration.TotalHours -ge 1) {
        return '{0} h {1} min' -f [int][math]::Floor($Duration.TotalHours), $Duration.Minutes
    }
    if ($Duration.TotalMinutes -ge 1) {
        return '{0} min {1} s' -f $Duration.Minutes, $Duration.Seconds
    }
    '{0} s' -f [int][math]::Max(1, [math]::Round($Duration.TotalSeconds))
}
