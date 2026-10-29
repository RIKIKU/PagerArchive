#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Watch-PagerArchiveMessage (R63b, ADR 0010) against a test server on a real
# local socket, played by this script: the request, chunked replies,
# catch-up dedupe, backoff and the warnings, refusals, renewals, silence,
# the proxy, plain HTTP and certificate checks, Ctrl+C and what -Verbose
# and -Debug show. The watch runs in its own runspace, so this
# thread can answer it; Wait-PaRetry and Request-PaToken are replaced
# there, so backoff takes milliseconds and no Authentik is needed. A59 runs
# against the real stack in internal/stacktest.

BeforeAll {
    $script:manifest = Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1'
    $script:token = 'test-access-token-' + [guid]::NewGuid().ToString('N')
    # Every stream line from every watch, searched for secrets and page text at the end.
    $script:allStreams = [System.Collections.Generic.List[string]]::new()

    # A message as the stream sends it.
    function New-StreamMessage([long] $Id, [string] $Text = "SHED FIRE $Id @ 3 FICTION ST, MADEUPTON", [string] $Capcode = '0100001') {
        [ordered]@{
            id = $Id; capcode = $Capcode; text = "@@$Text"; display_text = $Text; priority = 'Emergency'; heartbeat = $false
            sent_at = '2026-10-22T01:02:03Z'; alias_id = 1; alias = 'Station 1'; agency = 'Example Fire Service'; icon = $null; colour = $null
            ignored = $false; category = $null; format = $null; job_number = $null; event_type = $null; description = $null
            address = '3 FICTION ST'; locality = 'MADEUPTON'; grid_ref = $null; group = $null; units = $null; extra = @{}; labels = @{}
            location = $null; flags = @{ parse_failed = $false; address_unsplit = $false; location_mismatch = $false }
            receptions = @(@{ client = 'Stack receiver'; received_at = '2026-10-22T01:02:03Z' })
        } | ConvertTo-Json -Compress -Depth 4
    }

    # Starts a watch in its own runspace, connected to the test server. A
    # terminating error ends the runspace's script, so it's in Streams.Error
    # and the state is Completed.
    function Start-TestWatch {
        param([int] $Port, [hashtable] $Parameters = @{}, [double] $Silence = 60, [switch] $RenewalFails, [string] $Uri = "http://127.0.0.1:$Port", [bool] $AllowUnencrypted = $true)
        $secure = [securestring]::new()
        foreach ($c in $script:token.ToCharArray()) { $secure.AppendChar($c) }
        $connection = @{
            Uri = $Uri; AccessToken = $secure; ExpiresAt = [datetimeoffset]::UtcNow.AddHours(1)
            Store = 'session'; AllowUnencrypted = $AllowUnencrypted; RenewalFails = [bool]$RenewalFails
        }
        $waits = [System.Collections.Concurrent.ConcurrentQueue[int]]::new()
        $renewals = [System.Collections.Concurrent.ConcurrentQueue[datetimeoffset]]::new()
        $ps = [powershell]::Create()
        $null = $ps.AddScript({
                param($Manifest, $Connection, $Waits, $Renewals, $Silence, $Parameters)
                Import-Module -Name $Manifest -Force
                & (Get-Module -Name PagerArchive) {
                    param($Connection, $Waits, $Renewals, $Silence)
                    $script:Connection = $Connection
                    $script:WatchSilenceSec = $Silence
                    $script:TestWaits = $Waits
                    $script:TestRenewals = $Renewals
                    function script:Wait-PaRetry {
                        param([int] $Seconds, [System.Threading.CancellationToken] $Stop)
                        $script:TestWaits.Enqueue($Seconds)
                        Start-Sleep -Milliseconds 20
                    }
                    function script:Request-PaToken {
                        param([hashtable] $Connection)
                        $script:TestRenewals.Enqueue([datetimeoffset]::UtcNow)
                        if ($Connection.RenewalFails) {
                            throw (New-PaErrorRecord -ErrorId 'sign_in_ended' -Category AuthenticationError -Message 'Your PagerArchive sign-in has ended. Run Connect-PagerArchive.')
                        }
                        $Connection.ExpiresAt = [datetimeoffset]::UtcNow.AddHours(1)
                    }
                } $Connection $Waits $Renewals $Silence
                Watch-PagerArchiveMessage @Parameters -Verbose -Debug -InformationAction Continue
            }).AddParameters(@{ Manifest = $script:manifest; Connection = $connection; Waits = $waits; Renewals = $renewals; Silence = $Silence; Parameters = $Parameters })
        $output = [System.Management.Automation.PSDataCollection[psobject]]::new()
        $handle = $ps.BeginInvoke([System.Management.Automation.PSDataCollection[psobject]]::new(), $output)
        @{ PS = $ps; Output = $output; Handle = $handle; Waits = $waits; Renewals = $renewals; Connection = $connection }
    }

    # Stops a runspace's command as Ctrl+C does, waiting at most TimeoutMs:
    # PowerShell.Stop() waits for as long as the command takes to stop, so
    # a command that never stops would hang the tests instead of failing one.
    function Stop-TestRunspace([powershell] $PS, [int] $TimeoutMs = 10000) {
        if ($PS.InvocationStateInfo.State -notin 'Running', 'Stopping') {
            return
        }
        $stopping = $PS.BeginStop($null, $null)
        if (-not $stopping.AsyncWaitHandle.WaitOne($TimeoutMs)) {
            # What it was last doing, from its -Verbose and -Debug lines.
            $last = foreach ($stream in $PS.Streams.Verbose, $PS.Streams.Debug) {
                for ($i = [math]::Max(0, $stream.Count - 3); $i -lt $stream.Count; $i++) { $stream[$i].Message }
            }
            # And where it is: its script call stack, innermost first.
            $where = try {
                @($PS.Runspace.Debugger.GetCallStack() | ForEach-Object -Process { "$($_.FunctionName):$($_.ScriptLineNumber)" }) -join ' < '
            } catch {
                "unknown ($($_.Exception.Message))"
            }
            Save-TestDump
            throw "The watch didn't stop within $TimeoutMs ms of Ctrl+C; state $($PS.InvocationStateInfo.State); at $where; last lines: $($last -join ' | ')"
        }
        $PS.EndStop($stopping)
    }

    # With PA_TEST_DUMP_DIR set (CI), a dump of this process, with the stuck
    # watch's thread in it, for dotnet-dump; once per run. Linux lets a
    # process be read only by its parent or root (Yama), hence sudo.
    function Save-TestDump {
        if ($script:dumped -or -not $env:PA_TEST_DUMP_DIR) {
            return
        }
        $script:dumped = $true
        $tool = Join-Path -Path $PSHOME -ChildPath $(if ($IsWindows) { 'createdump.exe' } else { 'createdump' })
        $null = New-Item -ItemType Directory -Force -Path $env:PA_TEST_DUMP_DIR
        $file = Join-Path -Path $env:PA_TEST_DUMP_DIR -ChildPath "pwsh-$PID.dmp"
        if (-not $IsWindows -and (Get-Command -Name sudo -ErrorAction Ignore)) {
            & sudo -n $tool --withheap -f $file $PID | Out-Host
        } else {
            & $tool --withheap -f $file $PID | Out-Host
        }
        Write-Host "A dump of the test process, with the stuck watch: $file"
    }

    # Closes a runspace whose command has ended, waiting at most TimeoutMs.
    function Close-TestRunspace([powershell] $PS, [int] $TimeoutMs = 10000) {
        $PS.Runspace.CloseAsync()
        $timer = [System.Diagnostics.Stopwatch]::StartNew()
        while ($PS.Runspace.RunspaceStateInfo.State -notin 'Closed', 'Broken') {
            if ($timer.ElapsedMilliseconds -ge $TimeoutMs) {
                throw "The watch's runspace didn't close within $TimeoutMs ms; state $($PS.Runspace.RunspaceStateInfo.State)."
            }
            Start-Sleep -Milliseconds 20
        }
        $PS.Dispose()
    }

    # Ends a watch and keeps its streams' text for the secrets check.
    function Stop-TestWatch($Watch) {
        try {
            Stop-TestRunspace $Watch.PS
        } finally {
            foreach ($record in $Watch.PS.Streams.Verbose, $Watch.PS.Streams.Debug, $Watch.PS.Streams.Warning, $Watch.PS.Streams.Information, $Watch.PS.Streams.Error | ForEach-Object -Process { Get-TestItem $_ }) {
                $script:allStreams.Add([string]$record)
            }
            if ($Watch.PS.InvocationStateInfo.Reason) {
                $script:allStreams.Add([string]$Watch.PS.InvocationStateInfo.Reason)
            }
        }
        Close-TestRunspace $Watch.PS
    }

    # The items of a runspace's stream or output, by index: enumerating one
    # still open blocks until it closes.
    function Get-TestItem([object] $Collection) {
        for ($i = 0; $i -lt $Collection.Count; $i++) { $Collection[$i] }
    }

    function Wait-Until([scriptblock] $Condition, [int] $TimeoutMs = 10000, [string] $Because = 'the condition') {
        $timer = [System.Diagnostics.Stopwatch]::StartNew()
        while (-not (& $Condition)) {
            if ($timer.ElapsedMilliseconds -ge $TimeoutMs) {
                $seen = if ($watch) { ($watch.PS.Streams.Verbose, $watch.PS.Streams.Debug, $watch.PS.Streams.Warning, $watch.PS.Streams.Error | ForEach-Object -Process { Get-TestItem $_ }) -join ' | ' } else { '' }
                throw "Timed out waiting for $Because; state $($watch.PS.InvocationStateInfo.State); streams: $seen"
            }
            Start-Sleep -Milliseconds 20
        }
    }

    function Start-TestServer {
        $listener = [System.Net.Sockets.TcpListener]::new([ipaddress]::Loopback, 0)
        $listener.Start()
        $listener
    }

    # Accepts the watch's next connection and reads its request.
    function Receive-TestRequest($Listener, [int] $TimeoutMs = 10000) {
        $task = $Listener.AcceptTcpClientAsync()
        if (-not $task.Wait($TimeoutMs)) { throw 'The watch didn''t connect.' }
        $client = $task.Result
        $stream = $client.GetStream()
        $stream.ReadTimeout = $TimeoutMs
        $stream.WriteTimeout = $TimeoutMs
        $bytes = [System.Collections.Generic.List[byte]]::new()
        $buffer = [byte[]]::new(4096)
        while (-not [System.Text.Encoding]::ASCII.GetString($bytes.ToArray()).Contains("`r`n`r`n")) {
            $n = $stream.Read($buffer, 0, $buffer.Length)
            if ($n -eq 0) { break }
            $bytes.AddRange([System.ArraySegment[byte]]::new($buffer, 0, $n))
        }
        @{ Client = $client; Stream = $stream; Request = [System.Text.Encoding]::ASCII.GetString($bytes.ToArray()) }
    }

    function Send-TestBytes($Conn, [byte[]] $Bytes) {
        $Conn.Stream.Write($Bytes, 0, $Bytes.Length)
        $Conn.Stream.Flush()
    }

    # A streaming reply's head, as Go sends it (chunked).
    function Send-TestHead($Conn) {
        Send-TestBytes $Conn ([System.Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK`r`nContent-Type: text/event-stream`r`nCache-Control: no-cache`r`nTransfer-Encoding: chunked`r`n`r`n"))
        Send-TestChunk $Conn "retry: 3000`n: connected`n`n"
    }

    function Send-TestChunk($Conn, [string] $Text) {
        $data = [System.Text.Encoding]::UTF8.GetBytes($Text)
        Send-TestBytes $Conn ([System.Text.Encoding]::ASCII.GetBytes(('{0:x}' -f $data.Length) + "`r`n") + $data + [byte[]](13, 10))
    }

    function Send-TestEvent($Conn, [string] $Type, [string] $Data, [string] $Id) {
        $text = if ($Id) { "id: $Id`n" } else { '' }
        Send-TestChunk $Conn ($text + "event: $Type`ndata: $Data`n`n")
    }

    function Send-TestReady($Conn, [long] $LastId, [long] $CaughtUp = 0) {
        Send-TestEvent $Conn 'ready' ('{"caught_up":' + $CaughtUp + ',"last_id":' + $LastId + '}')
    }

    function Send-TestCatchUp($Conn, [long] $Count) {
        Send-TestEvent $Conn 'catch-up' ('{"count":' + $Count + '}')
    }

    function Send-TestRefusal($Conn, [int] $Status, [string] $Code, [string] $Message) {
        $body = [System.Text.Encoding]::UTF8.GetBytes((@{ error = $Message; code = $Code } | ConvertTo-Json -Compress))
        Send-TestBytes $Conn ([System.Text.Encoding]::ASCII.GetBytes("HTTP/1.1 $Status Refused`r`nContent-Type: application/json`r`nContent-Length: $($body.Length)`r`n`r`n") + $body)
        $Conn.Client.Dispose()
    }

    function Get-Query([string] $Request) {
        $target = ($Request -split "`r`n")[0] -replace '^GET (\S+) HTTP/1\.1$', '$1'
        $out = @{}
        foreach ($pair in ($target -split '\?', 2)[1] -split '&') {
            $name, $value = $pair -split '=', 2
            $out[$name] = @($out[$name]) + [uri]::UnescapeDataString($value) | Where-Object -FilterScript { $null -ne $_ }
        }
        $out
    }
}

Describe 'Watch-PagerArchiveMessage' {
    BeforeEach {
        $listener = Start-TestServer
        $port = $listener.LocalEndpoint.Port
        $watch = $null
    }
    AfterEach {
        if ($watch) { Stop-TestWatch $watch }
        $listener.Stop()
    }

    It 'asks for the stream with the bearer token and the filters, and writes each message the moment it arrives' {
        $watch = Start-TestWatch -Port $port -Parameters @{ Capcode = '0100001'; Priority = 'Emergency'; Alias = 'Station*' }
        $conn = Receive-TestRequest $listener
        $conn.Request | Should -Match "^GET /api/v1/stream\?\S+ HTTP/1\.1`r`n"
        $conn.Request | Should -Match "`r`nAuthorization: Bearer $([regex]::Escape($script:token))`r`n"
        $conn.Request | Should -Match "`r`nAccept: text/event-stream`r`n"
        $query = Get-Query $conn.Request
        $query.capcode | Should -Be '0100001'
        $query.priority | Should -Be 'Emergency'
        $query.alias_like | Should -Be 'Station%'
        $query.receptions | Should -Be 'true'
        $query.ContainsKey('after_id') | Should -BeFalse
        Send-TestHead $conn
        Send-TestReady $conn -LastId 40
        Send-TestEvent $conn 'message' (New-StreamMessage 41) '41'
        Wait-Until { $watch.Output.Count -eq 1 } -Because 'the first message'
        $watch.Output[0].PSObject.TypeNames[0] | Should -Be 'PagerArchive.Message'
        $watch.Output[0].Id | Should -Be 41
        $watch.Output[0].Text | Should -Be 'SHED FIRE 41 @ 3 FICTION ST, MADEUPTON'
        $watch.Output[0].Receptions[0].Client | Should -Be 'Stack receiver'
        # An event split across chunks, mid-line and mid-character.
        $text = (New-StreamMessage 42 'GRASS FIRE → É') -replace '^', "id: 42`nevent: message`ndata: "
        $half = [int]($text.Length / 2)
        Send-TestChunk $conn $text.Substring(0, $half)
        Start-Sleep -Milliseconds 100
        Send-TestChunk $conn ($text.Substring($half) + "`n`n")
        Wait-Until { $watch.Output.Count -eq 2 } -Because 'the second message'
        $watch.Output[1].Text | Should -Be 'GRASS FIRE → É'
        $watch.PS.InvocationStateInfo.State | Should -Be 'Running'
        $conn.Client.Dispose()
    }

    It 'catches up after a reconnect: after_id, then every message once (catch-up dedupe)' {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 10
        foreach ($id in 11, 12) { Send-TestEvent $conn 'message' (New-StreamMessage $id) "$id" }
        Wait-Until { $watch.Output.Count -eq 2 } -Because 'two messages'
        $conn.Client.Dispose()  # the connection drops

        $conn = Receive-TestRequest $listener
        (Get-Query $conn.Request).after_id | Should -Be '12'
        Send-TestHead $conn
        # The server repeats 12, catches up 13, and the live stream repeats 13.
        Send-TestCatchUp $conn 2
        foreach ($id in 12, 13) { Send-TestEvent $conn 'message' (New-StreamMessage $id) "$id" }
        Send-TestReady $conn -LastId 13 -CaughtUp 2
        foreach ($id in 13, 14, 11) { Send-TestEvent $conn 'message' (New-StreamMessage $id) "$id" }
        Send-TestEvent $conn 'message' (New-StreamMessage 15) '15'
        Wait-Until { $watch.Output.Count -ge 5 } -Because 'five messages'
        Start-Sleep -Milliseconds 200
        (Get-TestItem $watch.Output).Id | Should -Be @(11, 12, 13, 14, 15)

        # Before any message, ready's last_id is where the next catch-up starts.
        $conn.Client.Dispose()
        $conn = Receive-TestRequest $listener
        (Get-Query $conn.Request).after_id | Should -Be '15'
        Send-TestHead $conn
        Send-TestReady $conn -LastId 20
        $conn.Client.Dispose()
        $conn = Receive-TestRequest $listener
        (Get-Query $conn.Request).after_id | Should -Be '20'
        $conn.Client.Dispose()
    }

    It 'retries with backoff: one warning, a verbose line per retry, a second warning when back, before what follows' {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        Start-Sleep -Milliseconds 200
        $conn.Client.Dispose()
        # Down: 7 tries are refused (the connection closes before a reply;
        # HttpClient itself makes up to 4 connections for each try).
        while ($true) {
            $conn = Receive-TestRequest $listener
            if ($watch.Waits.Count -ge 8) { break }
            $conn.Client.Dispose()
        }
        Send-TestHead $conn
        Send-TestCatchUp $conn 3
        Wait-Until { @(Get-TestItem $watch.PS.Streams.Warning).Count -ge 2 } -Because 'the reconnected warning'
        $watch.Output.Count | Should -Be 0 -Because 'the warning comes before the messages that follow'
        $watch.Waits.ToArray() | Should -Be @(1, 2, 4, 8, 16, 30, 30, 30)
        $watch.PS.Streams.Warning[0].Message | Should -Match "^Lost the connection to PagerArchive at http://127\.0\.0\.1:$port \(the server closed the connection\)\. Retrying until it's back"
        $watch.PS.Streams.Warning[1].Message | Should -Match '^Reconnected after \d+ s; 3 messages that arrived meanwhile follow\.$'
        @(Get-TestItem $watch.PS.Streams.Verbose | Where-Object -Property Message -Match '^Reconnecting to PagerArchive in \d+ s \(attempt \d\)\.$').Count | Should -Be 8
        foreach ($id in 2, 3, 4) { Send-TestEvent $conn 'message' (New-StreamMessage $id) "$id" }
        Send-TestReady $conn -LastId 4 -CaughtUp 3
        Wait-Until { $watch.Output.Count -eq 3 } -Because 'the messages that arrived meanwhile'
        Start-Sleep -Milliseconds 200
        @(Get-TestItem $watch.PS.Streams.Warning).Count | Should -Be 2
        @(Get-TestItem $watch.PS.Streams.Information).Count | Should -Be 0
        $watch.PS.InvocationStateInfo.State | Should -Be 'Running'
        $conn.Client.Dispose()
    }

    It 'says how many follow when back: <Count>' -ForEach @(
        @{ Count = 0; Expected = 'no messages arrived meanwhile' }
        @{ Count = 1; Expected = '1 message that arrived meanwhile follows' }
        @{ Count = 1234; Expected = '1?234 messages that arrived meanwhile follow' }
    ) {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        $conn.Client.Dispose()
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestCatchUp $conn $Count
        Wait-Until { @(Get-TestItem $watch.PS.Streams.Warning).Count -ge 2 } -Because 'the reconnected warning'
        $watch.PS.Streams.Warning[1].Message | Should -BeLike "Reconnected after * s; $Expected."
        $conn.Client.Dispose()
    }

    It 'warns how many follow when more than 1,000 arrived during a quiet reconnect' {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        Send-TestEvent $conn 'reconnect' '{"reason":"behind"}'
        $conn.Client.Dispose()
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestCatchUp $conn 1500
        Wait-Until { @(Get-TestItem $watch.PS.Streams.Warning).Count -ge 1 } -Because 'the catch-up warning'
        $watch.PS.Streams.Warning[0].Message | Should -BeLike '1?500 messages arrived while reconnecting; they follow, oldest first.'
        @(Get-TestItem $watch.PS.Streams.Warning).Count | Should -Be 1
        $conn.Client.Dispose()
    }

    It 'goes through the system''s proxy, as every other command does' {
        $default = [System.Net.Http.HttpClient]::DefaultProxy
        try {
            # The listener plays an HTTP proxy; the server's name doesn't resolve.
            [System.Net.Http.HttpClient]::DefaultProxy = [System.Net.WebProxy]::new("http://127.0.0.1:$port")
            $watch = Start-TestWatch -Port $port -Uri 'http://pagerarchive.invalid:8095'
            $conn = Receive-TestRequest $listener
            $conn.Request | Should -Match "^GET http://pagerarchive\.invalid:8095/api/v1/stream\?\S+ HTTP/1\.1`r`n"
            $conn.Request | Should -Match "`r`nAuthorization: Bearer $([regex]::Escape($script:token))`r`n"
            Send-TestHead $conn
            Send-TestReady $conn -LastId 1
            Send-TestEvent $conn 'message' (New-StreamMessage 2) '2'
            Wait-Until { $watch.Output.Count -eq 1 } -Because 'a message through the proxy'
            $conn.Client.Dispose()
        } finally {
            [System.Net.Http.HttpClient]::DefaultProxy = $default
        }
    }

    It 'refuses plain HTTP unless the sign-in allowed it (-AllowUnencryptedAuthentication), before connecting' {
        $watch = Start-TestWatch -Port $port -AllowUnencrypted $false
        Wait-Until { $watch.PS.InvocationStateInfo.State -ne 'Running' } -Because 'the watch to stop'
        $watch.PS.Streams.Error[0].FullyQualifiedErrorId | Should -Be 'unencrypted_refused,Watch-PagerArchiveMessage'
        $listener.Pending() | Should -BeFalse -Because 'nothing was sent'
    }

    It 'checks the server''s certificate as strictly as every other command' {
        # A certificate for localhost that nothing trusts.
        $key = [System.Security.Cryptography.RSA]::Create(2048)
        $csr = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new('CN=localhost', $key, 'SHA256', [System.Security.Cryptography.RSASignaturePadding]::Pkcs1)
        $names = [System.Security.Cryptography.X509Certificates.SubjectAlternativeNameBuilder]::new()
        $names.AddDnsName('localhost')
        $csr.CertificateExtensions.Add($names.Build())
        $made = $csr.CreateSelfSigned([datetimeoffset]::UtcNow.AddDays(-1), [datetimeoffset]::UtcNow.AddDays(1))
        $cert = [System.Security.Cryptography.X509Certificates.X509CertificateLoader]::LoadPkcs12($made.Export('Pfx'), $null)
        $watch = Start-TestWatch -Port $port -Uri "https://localhost:$port"
        $task = $listener.AcceptTcpClientAsync()
        $task.Wait(10000) | Should -BeTrue
        $ssl = [System.Net.Security.SslStream]::new($task.Result.GetStream(), $false)
        try {
            $ssl.AuthenticateAsServerAsync($cert).Wait(10000)
        } catch {
            $null = $_  # the watch refuses the certificate
        }
        Wait-Until { $watch.PS.InvocationStateInfo.State -ne 'Running' } -Because 'the watch to stop'
        $err = $watch.PS.Streams.Error[0]
        $err.FullyQualifiedErrorId | Should -Be 'server_unreachable,Watch-PagerArchiveMessage'
        $err.Exception.Message | Should -Match 'certificate'
        $ssl.Dispose()
        $task.Result.Dispose()
    }

    It 'stops with the API''s error when <Case>, without retrying' -ForEach @(
        @{ Case = 'deactivated'; Status = 401; Code = 'deactivated'; Category = 'AuthenticationError'; Message = 'Your account has been deactivated. Ask an admin if you think this is a mistake.' }
        @{ Case = 'demoted'; Status = 403; Code = 'powershell_admins_only'; Category = 'PermissionDenied'; Message = 'PowerShell access is for admins.' }
        @{ Case = 'the account is deleted'; Status = 401; Code = 'not_signed_in'; Category = 'AuthenticationError'; Message = 'No PagerArchive account for this sign-in.' }
    ) {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        Send-TestEvent $conn 'reconnect' '{"reason":"account"}'
        $conn.Client.Dispose()
        $conn = Receive-TestRequest $listener
        Send-TestRefusal $conn -Status $Status -Code $Code -Message $Message
        Wait-Until { $watch.PS.InvocationStateInfo.State -ne 'Running' } -Because 'the watch to stop'
        $watch.PS.InvocationStateInfo.State | Should -Be 'Completed'
        @(Get-TestItem $watch.PS.Streams.Error).Count | Should -Be 1
        $err = $watch.PS.Streams.Error[0]
        $err.FullyQualifiedErrorId | Should -Be "$Code,Watch-PagerArchiveMessage"
        $err.CategoryInfo.Category | Should -Be $Category
        $err.Exception.Message | Should -Be $Message
        $watch.Waits.Count | Should -Be 0
        @(Get-TestItem $watch.PS.Streams.Warning).Count | Should -Be 0
    }

    It 'renews its token quietly when the stream ends at the token''s expiry, and stops with sign_in_ended when renewal is refused' {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 5
        $watch.Connection.ExpiresAt = [datetimeoffset]::UtcNow.AddSeconds(-1)
        Send-TestEvent $conn 'reconnect' '{"reason":"expired"}'
        $conn.Client.Dispose()
        $conn = Receive-TestRequest $listener
        $watch.Renewals.Count | Should -Be 1
        (Get-Query $conn.Request).after_id | Should -Be '5'
        Send-TestHead $conn
        Send-TestReady $conn -LastId 6 -CaughtUp 1
        Wait-Until { (Get-TestItem $watch.PS.Streams.Verbose).Message -match '^Caught up: 1 messages' } -Because 'the catch-up'
        @(Get-TestItem $watch.PS.Streams.Warning).Count | Should -Be 0
        @(Get-TestItem $watch.PS.Streams.Information).Count | Should -Be 0

        # Revoked in Authentik: the next renewal is refused.
        $watch.Connection.RenewalFails = $true
        $watch.Connection.ExpiresAt = [datetimeoffset]::UtcNow.AddSeconds(-1)
        Send-TestEvent $conn 'reconnect' '{"reason":"expired"}'
        $conn.Client.Dispose()
        Wait-Until { $watch.PS.InvocationStateInfo.State -ne 'Running' } -Because 'the watch to stop'
        $watch.PS.Streams.Error[0].FullyQualifiedErrorId | Should -Be 'sign_in_ended,Watch-PagerArchiveMessage'
        $watch.Waits.Count | Should -Be 0
        @(Get-TestItem $watch.PS.Streams.Warning).Count | Should -Be 0
    }

    It 'renews once and tries again when the API refuses the token as invalid' {
        $watch = Start-TestWatch -Port $port
        Send-TestRefusal (Receive-TestRequest $listener) -Status 401 -Code 'invalid_token' -Message 'expired'
        $conn = Receive-TestRequest $listener
        $watch.Renewals.Count | Should -Be 1
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        Start-Sleep -Milliseconds 200
        $watch.PS.InvocationStateInfo.State | Should -Be 'Running'
        $conn.Client.Dispose()
    }

    It 'counts 60 seconds of silence as a lost connection (here 1 second)' {
        $watch = Start-TestWatch -Port $port -Silence 1
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        $next = Receive-TestRequest $listener -TimeoutMs 5000
        $watch.PS.Streams.Warning[0].Message | Should -Match '\(nothing arrived for 1 s\)'
        $conn.Client.Dispose()
        $next.Client.Dispose()
    }

    It 'stops at its first connection''s failure: nothing to resume yet' {
        $listener.Stop()
        $watch = Start-TestWatch -Port $port
        Wait-Until { $watch.PS.InvocationStateInfo.State -ne 'Running' } -Because 'the watch to stop'
        $watch.PS.Streams.Error[0].FullyQualifiedErrorId | Should -Be 'server_unreachable,Watch-PagerArchiveMessage'
        $watch.PS.Streams.Error[0].CategoryInfo.Category | Should -Be 'ConnectionError'
        $watch.Waits.Count | Should -Be 0
    }

    It 'stops on Ctrl+C with no error, and closes the connection' {
        $watch = Start-TestWatch -Port $port
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        Send-TestEvent $conn 'message' (New-StreamMessage 2) '2'
        Wait-Until { $watch.Output.Count -eq 1 } -Because 'a message'
        Stop-TestRunspace $watch.PS  # what Ctrl+C calls
        $watch.PS.InvocationStateInfo.State | Should -Be 'Stopped'
        @(Get-TestItem $watch.PS.Streams.Error).Count | Should -Be 0
        $conn.Stream.ReadTimeout = 3000
        $conn.Stream.Read([byte[]]::new(16), 0, 16) | Should -Be 0 -Because 'the watch closed its socket'
        $conn.Client.Dispose()
    }

    It 'closes the connection when a later command stops it (Select-Object -First)' {
        $ps = [powershell]::Create()
        $null = $ps.AddScript({
                param($Manifest, $Port, $Token)
                Import-Module -Name $Manifest -Force
                & (Get-Module -Name PagerArchive) {
                    param($Port, $Token)
                    $secure = [securestring]::new()
                    foreach ($c in $Token.ToCharArray()) { $secure.AppendChar($c) }
                    $script:Connection = @{ Uri = "http://127.0.0.1:$Port"; AccessToken = $secure; ExpiresAt = [datetimeoffset]::UtcNow.AddHours(1); Store = 'session'; AllowUnencrypted = $true }
                } $Port $Token
                Watch-PagerArchiveMessage | Select-Object -First 2
            }).AddParameters(@{ Manifest = $script:manifest; Port = $port; Token = $script:token })
        $handle = $ps.BeginInvoke()
        $conn = Receive-TestRequest $listener
        Send-TestHead $conn
        Send-TestReady $conn -LastId 1
        foreach ($id in 2, 3) { Send-TestEvent $conn 'message' (New-StreamMessage $id) "$id" }
        try {
            $handle.AsyncWaitHandle.WaitOne(10000) | Should -BeTrue -Because 'Select-Object -First 2 stops the watch after two messages'
            $out = $ps.EndInvoke($handle)
            $out.Id | Should -Be @(2, 3)
            @(Get-TestItem $ps.Streams.Error).Count | Should -Be 0
            $conn.Stream.ReadTimeout = 3000
            $conn.Stream.Read([byte[]]::new(16), 0, 16) | Should -Be 0 -Because 'the watch closed its socket'
        } finally {
            $conn.Client.Dispose()
            Stop-TestRunspace $ps
            Close-TestRunspace $ps
        }
    }
}

# PowerShell loses a Ctrl+C that arrives while a command runs its own finally
# block (ADR 0012): it clears the pipeline's stopping flag for the block and
# restores the old value afterwards. The watch never ends by itself, so it
# has no finally block, and its waits also check PSCmdlet.PipelineStopToken.
Describe 'Watch-PagerArchiveMessage and a lost Ctrl+C' {
    BeforeAll {
        # Runs Script in its own runspace with the module loaded, stops it
        # after DelayMs if it's still running, and says how it ended.
        function Invoke-TestStop([scriptblock] $Script, [int] $DelayMs = 300) {
            $ps = [powershell]::Create()
            $null = $ps.AddScript("Import-Module -Name '$script:manifest' -Force").AddScript($Script)
            $handle = $ps.BeginInvoke()
            Start-Sleep -Milliseconds $DelayMs
            $timer = [System.Diagnostics.Stopwatch]::StartNew()
            Stop-TestRunspace $ps -TimeoutMs 5000
            $null = $handle.AsyncWaitHandle.WaitOne(5000)
            $out = @{ State = [string]$ps.InvocationStateInfo.State; Ms = $timer.ElapsedMilliseconds; Errors = @(Get-TestItem $ps.Streams.Error).Count }
            Close-TestRunspace $ps
            $out
        }
    }

    It 'has no finally block, where PowerShell would lose a Ctrl+C' {
        $ast = (Get-Command -Name Watch-PagerArchiveMessage -Module (Import-Module -Name $script:manifest -Force -PassThru)).ScriptBlock.Ast
        $finally = $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.TryStatementAst] -and $n.Finally }, $true)
        @($finally).Count | Should -Be 0 -Because 'a stop that arrives while the command runs its own finally block is lost, and the watch would never end'
    }

    It 'ends its waits as soon as the stop token is cancelled: <Name>' -ForEach @(
        @{ Name = 'Wait-PaTask'; Script = { & (Get-Module -Name PagerArchive) { $c = [System.Threading.CancellationTokenSource]::new(); $c.CancelAfter(100); Wait-PaTask -Task ([System.Threading.Tasks.Task]::Delay(60000)) -Stop $c.Token } } }
        @{ Name = 'Wait-PaRetry'; Script = { & (Get-Module -Name PagerArchive) { $c = [System.Threading.CancellationTokenSource]::new(); $c.CancelAfter(100); Wait-PaRetry -Seconds 30 -Stop $c.Token } } }
    ) {
        # No Stop() here: only the token, as when PowerShell has lost the stop.
        $ps = [powershell]::Create()
        $null = $ps.AddScript("Import-Module -Name '$script:manifest' -Force").AddScript($Script)
        $handle = $ps.BeginInvoke()
        $handle.AsyncWaitHandle.WaitOne(10000) | Should -BeTrue -Because 'the wait ends when the token is cancelled'
        $ps.InvocationStateInfo.State | Should -Be 'Stopped'
        Close-TestRunspace $ps
    }

    It 'hands its stop token to each wait: connecting and waiting to reconnect' {
        # A real watch whose stream is replaced: ready, then lost, so it
        # connects and then waits to reconnect. Each replacement notes
        # whether it was given a token a stop can cancel; without -Stop it
        # gets none, and nothing a stop could cancel.
        $seen = [System.Collections.Concurrent.ConcurrentDictionary[string, bool]]::new()
        $ps = [powershell]::Create()
        $null = $ps.AddScript({
                param($Manifest, $Seen)
                Import-Module -Name $Manifest -Force
                & (Get-Module -Name PagerArchive) {
                    param($Seen)
                    $secure = [securestring]::new()
                    foreach ($c in 'test-token'.ToCharArray()) { $secure.AppendChar($c) }
                    $script:Connection = @{ Uri = 'http://127.0.0.1:9'; AccessToken = $secure; ExpiresAt = [datetimeoffset]::UtcNow.AddHours(1); Store = 'session'; AllowUnencrypted = $true }
                    $script:TestSeen = $Seen
                    function script:Open-PaEventStream {
                        param([hashtable] $Connection, [string] $PathAndQuery, [int] $TimeoutSec, [System.Threading.CancellationToken] $Stop)
                        $script:TestSeen['Open-PaEventStream'] = $PSBoundParameters.ContainsKey('Stop') -and $Stop.CanBeCanceled
                        @{ Status = 200; Read = $false }
                    }
                    function script:Read-PaStreamEvent {
                        param([hashtable] $State, [double] $SilenceSec)
                        if (-not $State.Read) {
                            $State.Read = $true
                            return @{ Type = 'ready'; Id = ''; Data = '{"caught_up":0,"last_id":1}' }
                        }
                        throw (New-PaErrorRecord -ErrorId 'connection_lost' -Category ConnectionError -Message 'the server closed the connection')
                    }
                    function script:Close-PaEventStream { param([hashtable] $State) }
                    function script:Wait-PaRetry {
                        param([int] $Seconds, [System.Threading.CancellationToken] $Stop)
                        $script:TestSeen['Wait-PaRetry'] = $PSBoundParameters.ContainsKey('Stop') -and $Stop.CanBeCanceled
                        throw (New-PaErrorRecord -ErrorId 'test_done' -Message 'The test ends the watch here.')
                    }
                } $Seen
                Watch-PagerArchiveMessage -WarningAction SilentlyContinue
            }).AddParameters(@{ Manifest = $script:manifest; Seen = $seen })
        $handle = $ps.BeginInvoke()
        $handle.AsyncWaitHandle.WaitOne(10000) | Should -BeTrue -Because 'the watch ends at the test''s Wait-PaRetry'
        $seen.Keys | Sort-Object | Should -Be @('Open-PaEventStream', 'Wait-PaRetry') -Because 'it connected, lost the stream and waited to reconnect'
        $seen['Open-PaEventStream'] | Should -BeTrue -Because 'Watch-PagerArchiveMessage passes its stop token to Open-PaEventStream (-Stop $stop)'
        $seen['Wait-PaRetry'] | Should -BeTrue -Because 'Watch-PagerArchiveMessage passes its stop token to Wait-PaRetry (-Stop $stop)'
        Close-TestRunspace $ps
    }

    It 'relies on a stop always cancelling PipelineStopToken, even one PowerShell loses' {
        # A command that spends its time in its own finally block, ending on the token alone.
        $result = Invoke-TestStop -Script {
            function Test-Loop {
                [CmdletBinding()]
                param()
                end {
                    while (-not $PSCmdlet.PipelineStopToken.IsCancellationRequested) {
                        try { $null = 1 } finally { [System.Threading.Thread]::Sleep(50) }
                    }
                }
            }
            Test-Loop
        }
        $result.State | Should -BeIn 'Stopped', 'Completed'
        $result.Ms | Should -BeLessThan 3000
    }
}

Describe 'Watch-PagerArchiveMessage output streams' {
    It 'never shows the token or a page''s text in -Verbose, -Debug, warnings, information or errors' {
        $script:allStreams.Count | Should -BeGreaterThan 20
        ($script:allStreams | Where-Object -FilterScript { $_ -match 'Reconnecting|GET /api/v1/stream|event message' }).Count | Should -BeGreaterThan 0
        foreach ($line in $script:allStreams) {
            $line | Should -Not -Match ([regex]::Escape($script:token))
            $line | Should -Not -Match 'SHED FIRE|GRASS FIRE|FICTION ST|MADEUPTON'
        }
    }
}
