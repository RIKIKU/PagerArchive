# The live stream (GET /api/v1/stream) for Watch-PagerArchiveMessage (ADR
# 0010 section 6). Invoke-RestMethod reads a whole response before it
# returns, and HttpClient takes a bearer token only as a string, so this
# speaks HTTP/1.1 itself over a socket: the request is built as bytes, with
# the access token copied straight out of its SecureString, and the buffer is
# zeroed once sent. It reads the status, the headers and a chunked or plain
# body a line at a time. Every wait is a slice of 200 ms, so Ctrl+C can stop
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

function Open-PaEventStream {
    <#
    .SYNOPSIS
    Opens the live stream as the signed-in admin and reads the reply's status and headers.

    .DESCRIPTION
    Returns the stream's state, for Read-PaStreamEvent and
    Close-PaEventStream. Status is the HTTP status; for anything but 200, the
    body is read too, and Code and Message hold the API's refusal. Failing
    to connect, or a reply that isn't HTTP, throws a ConnectionError.
    #>
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
    $state = @{
        Uri       = $uri
        Tcp       = $null
        Stream    = $null
        Pending   = $null
        Buffer    = [byte[]]::new(16384)
        Raw       = [System.Collections.Generic.List[byte]]::new()
        Body      = [System.Collections.Generic.List[byte]]::new()
        Headers   = $false
        Chunked   = $false
        Left      = [long]-1
        AfterCrLf = $false
        BodyDone  = $false
        Closed    = $false
        LastData  = [System.Diagnostics.Stopwatch]::StartNew()
        Event     = @{ Type = ''; Id = ''; Data = [System.Collections.Generic.List[string]]::new() }
        Status    = 0
        Code      = $null
        Message   = $null
    }
    $origin = $uri.GetLeftPart([System.UriPartial]::Authority)
    $request = $null
    try {
        $state.Tcp = [System.Net.Sockets.TcpClient]::new()
        $state.Tcp.NoDelay = $true
        if (-not (Wait-PaTask -Task $state.Tcp.ConnectAsync($uri.DnsSafeHost, $uri.Port) -TimeoutMs ($TimeoutSec * 1000))) {
            throw [System.TimeoutException]::new("no answer within $TimeoutSec s")
        }
        $state.Stream = $state.Tcp.GetStream()
        if ($uri.Scheme -eq 'https') {
            $ssl = [System.Net.Security.SslStream]::new($state.Stream, $false)
            $state.Stream = $ssl
            if (-not (Wait-PaTask -Task $ssl.AuthenticateAsClientAsync($uri.IdnHost) -TimeoutMs ($TimeoutSec * 1000))) {
                throw [System.TimeoutException]::new("no TLS answer within $TimeoutSec s")
            }
        }
        $head = [System.Text.Encoding]::ASCII.GetBytes((
                "GET $PathAndQuery HTTP/1.1`r`nHost: $($uri.Authority)`r`nAccept: text/event-stream`r`nCache-Control: no-cache`r`n" +
                "User-Agent: PagerArchive-PowerShell`r`nConnection: close`r`nAuthorization: Bearer "))
        $token = $Connection.AccessToken
        $request = [byte[]]::new($head.Length + $token.Length + 4)
        [array]::Copy($head, $request, $head.Length)
        $pointer = [System.Runtime.InteropServices.Marshal]::SecureStringToGlobalAllocUnicode($token)
        try {
            for ($i = 0; $i -lt $token.Length; $i++) {
                $c = [System.Runtime.InteropServices.Marshal]::ReadInt16($pointer, $i * 2)
                if ($c -le 32 -or $c -ge 127) {
                    throw [System.InvalidOperationException]::new('The access token has a character a header can''t carry.')
                }
                $request[$head.Length + $i] = [byte]$c
            }
            $c = 0
        } finally {
            [System.Runtime.InteropServices.Marshal]::ZeroFreeGlobalAllocUnicode($pointer)
        }
        $tail = $head.Length + $token.Length
        $request[$tail] = 13
        $request[$tail + 1] = 10
        $request[$tail + 2] = 13
        $request[$tail + 3] = 10
        $null = Wait-PaTask -Task $state.Stream.WriteAsync($request, 0, $request.Length)
        [array]::Clear($request)

        # The status line and headers.
        $line = Read-PaRawLine -State $state -TimeoutSec $TimeoutSec
        if ($line -notmatch '^HTTP/1\.[01] (\d{3})') {
            throw [System.IO.InvalidDataException]::new('the reply isn''t HTTP')
        }
        $state.Status = [int]$Matches[1]
        while (($line = Read-PaRawLine -State $state -TimeoutSec $TimeoutSec) -ne '') {
            $name, $value = $line -split ':', 2
            switch ($name.Trim()) {
                'Transfer-Encoding' { $state.Chunked = $value -match 'chunked' }
                'Content-Length' { $state.Left = [long]$value.Trim() }
            }
        }
        $state.Headers = $true
        if ($state.Chunked) {
            $state.Left = 0
        }
        Move-PaBody -State $state
    } catch {
        if ($null -ne $request) {
            [array]::Clear($request)
        }
        Close-PaEventStream -State $state
        throw (New-PaErrorRecord -ErrorId 'server_unreachable' -Category ConnectionError -Message "PagerArchive can't be reached at ${origin}: $($_.Exception.GetBaseException().Message)")
    }
    if ($state.Status -ne 200) {
        # A refusal: read its JSON for the API's code and message.
        $text = $null
        try {
            $watch = [System.Diagnostics.Stopwatch]::StartNew()
            while (-not $state.BodyDone -and -not $state.Closed -and $watch.Elapsed.TotalSeconds -lt $TimeoutSec) {
                Receive-PaStreamData -State $state
            }
            $text = [System.Text.Encoding]::UTF8.GetString($state.Body.ToArray())
            if ($text -and $text.TrimStart().StartsWith('{')) {
                $reply = $text | ConvertFrom-Json -AsHashtable
                $state.Code = $reply['code']
                $state.Message = $reply['error']
            }
        } catch {
            $null = $_
        }
        Close-PaEventStream -State $state
    }
    $state
}

function Receive-PaStreamData {
    <#
    .SYNOPSIS
    Waits up to 200 ms for bytes from the socket, then decodes what it can of the body.
    #>
    [CmdletBinding()]
    param(
        # The stream's state.
        [Parameter(Mandatory)]
        [hashtable] $State
    )
    if (-not $State.Closed) {
        if ($null -eq $State.Pending) {
            $State.Pending = $State.Stream.ReadAsync($State.Buffer, 0, $State.Buffer.Length)
        }
        if (Wait-PaTask -Task $State.Pending -TimeoutMs 200) {
            $n = $State.Pending.Result
            $State.Pending = $null
            if ($n -eq 0) {
                $State.Closed = $true
            } else {
                $State.Raw.AddRange([System.ArraySegment[byte]]::new($State.Buffer, 0, $n))
                $State.LastData.Restart()
            }
        }
    }
    if ($State.Headers) {
        Move-PaBody -State $State
    }
}

function Read-PaRawLine {
    <#
    .SYNOPSIS
    Reads one line of the reply's head (or a chunk's size), without its line ending.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # The stream's state.
        [Parameter(Mandatory)]
        [hashtable] $State,

        # How long it may take.
        [int] $TimeoutSec = 30
    )
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    while (($i = $State.Raw.IndexOf([byte]10)) -lt 0) {
        if ($State.Closed) {
            throw [System.IO.EndOfStreamException]::new('the connection closed')
        }
        if ($watch.Elapsed.TotalSeconds -ge $TimeoutSec) {
            throw [System.TimeoutException]::new("no reply within $TimeoutSec s")
        }
        Receive-PaStreamData -State $State
    }
    $line = [System.Text.Encoding]::ASCII.GetString($State.Raw.GetRange(0, $i).ToArray()).TrimEnd("`r")
    $State.Raw.RemoveRange(0, $i + 1)
    $line
}

function Move-PaBody {
    <#
    .SYNOPSIS
    Moves the reply's body from the raw bytes, taking chunked framing off.
    #>
    [CmdletBinding()]
    param(
        # The stream's state.
        [Parameter(Mandatory)]
        [hashtable] $State
    )
    while (-not $State.BodyDone -and $State.Raw.Count -gt 0) {
        if (-not $State.Chunked) {
            $n = if ($State.Left -ge 0) { [math]::Min($State.Left, $State.Raw.Count) } else { $State.Raw.Count }
            $State.Body.AddRange($State.Raw.GetRange(0, $n))
            $State.Raw.RemoveRange(0, $n)
            if ($State.Left -ge 0) {
                $State.Left -= $n
                $State.BodyDone = $State.Left -eq 0
            }
            continue
        }
        if ($State.Left -gt 0) {
            $n = [int][math]::Min($State.Left, $State.Raw.Count)
            $State.Body.AddRange($State.Raw.GetRange(0, $n))
            $State.Raw.RemoveRange(0, $n)
            $State.Left -= $n
            $State.AfterCrLf = $State.Left -eq 0
            continue
        }
        # A chunk's size line, or the line ending after a chunk's data.
        $i = $State.Raw.IndexOf([byte]10)
        if ($i -lt 0) {
            break
        }
        $line = [System.Text.Encoding]::ASCII.GetString($State.Raw.GetRange(0, $i).ToArray()).Trim()
        $State.Raw.RemoveRange(0, $i + 1)
        if ($State.AfterCrLf) {
            $State.AfterCrLf = $false
            continue
        }
        $size = [Convert]::ToInt64(($line -split ';', 2)[0], 16)
        if ($size -eq 0) {
            $State.BodyDone = $true
        } else {
            $State.Left = $size
        }
    }
}

function Read-PaStreamEvent {
    <#
    .SYNOPSIS
    Reads the stream's next event.

    .DESCRIPTION
    Returns a hashtable: Type (message, caught-up, reconnect, ...), Id and
    Data. Comments (the server's keepalives) are read and skipped. Throws a
    ConnectionError (connection_lost) when the stream ends, a read fails, or
    nothing at all arrives for SilenceSec: the server sends a keepalive
    every 25 seconds, so silence means a connection that died without
    closing.
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
        if ($State.BodyDone -or ($State.Closed -and $State.Raw.Count -eq 0)) {
            $reason = 'the server closed the connection'
        } elseif ($State.LastData.Elapsed.TotalSeconds -ge $SilenceSec) {
            $reason = "nothing arrived for $([int]$SilenceSec) s"
        } else {
            try {
                Receive-PaStreamData -State $State
            } catch {
                $reason = $_.Exception.GetBaseException().Message
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
    Closes the stream's socket. Safe to call more than once.
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
    foreach ($thing in @($State.Stream, $State.Tcp)) {
        if ($null -ne $thing) {
            try {
                $thing.Dispose()
            } catch {
                $null = $_
            }
        }
    }
    $State.Stream = $null
    $State.Tcp = $null
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
