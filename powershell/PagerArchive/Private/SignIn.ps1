# Signing in (R60, ADR 0009 section 7): the browser with PKCE and a
# loopback redirect on a free port (RFC 8252), or a device code (RFC 8628).

function New-PaRandomString {
    <#
    .SYNOPSIS
    A random base64url string from the given number of bytes.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Makes an object in memory; changes nothing.')]
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # How many random bytes.
        [int] $Bytes = 32
    )
    $data = [System.Security.Cryptography.RandomNumberGenerator]::GetBytes($Bytes)
    [System.Convert]::ToBase64String($data).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Get-PaCodeChallenge {
    <#
    .SYNOPSIS
    The PKCE S256 challenge for a verifier.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # The verifier.
        [Parameter(Mandatory)]
        [string] $Verifier
    )
    $hash = [System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::ASCII.GetBytes($Verifier))
    [System.Convert]::ToBase64String($hash).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Start-PaBrowser {
    <#
    .SYNOPSIS
    Opens the system browser at a URL.
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Low')]
    param(
        # The URL.
        [Parameter(Mandatory)]
        [string] $Uri
    )
    if (-not $PSCmdlet.ShouldProcess('the browser', 'Open')) {
        return
    }
    if ($IsWindows) {
        Start-Process -FilePath $Uri
    } elseif ($IsMacOS) {
        Start-Process -FilePath 'open' -ArgumentList $Uri
    } else {
        Start-Process -FilePath 'xdg-open' -ArgumentList $Uri
    }
}

function Show-PaDeviceCode {
    <#
    .SYNOPSIS
    Tells the user where to enter the device code.

    .DESCRIPTION
    A warning, as Connect-AzAccount -UseDeviceAuthentication does: it's
    shown by default, and Write-Host isn't allowed.
    #>
    [CmdletBinding()]
    param(
        # Where to enter it.
        [Parameter(Mandatory)]
        [string] $VerificationUri,

        # The code.
        [Parameter(Mandatory)]
        [string] $UserCode
    )
    Write-Warning -Message "To sign in, open $VerificationUri on any device and enter the code $UserCode"
}

function Read-PaLoopbackRequest {
    <#
    .SYNOPSIS
    Reads the browser's request to the loopback redirect, and answers it.

    .DESCRIPTION
    Returns @{ Path; State; Error; Code } with Code a SecureString. Only
    the request line is read: GET /?code=...&state=... HTTP/1.1.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The accepted connection.
        [Parameter(Mandatory)]
        [System.Net.Sockets.TcpClient] $Client
    )
    $stream = $Client.GetStream()
    $stream.ReadTimeout = 5000
    $buffer = [byte[]]::new(8192)
    $count = 0
    try {
        $out = @{ Path = ''; State = ''; Error = ''; Code = $null }
        try {
            while ($count -lt $buffer.Length) {
                $n = $stream.Read($buffer, $count, $buffer.Length - $count)
                if ($n -le 0) { break }
                $count += $n
                # Enough once the request line has ended.
                if ([array]::IndexOf($buffer, [byte]10, 0, $count) -ge 0) { break }
            }
        } catch [System.IO.IOException] {
            # A browser's speculative connection that never sends a request.
            return $out
        }
        $end = [array]::IndexOf($buffer, [byte]10, 0, $count)
        if ($end -lt 0) { $end = $count }
        # GET <target> HTTP/1.1
        $first = [array]::IndexOf($buffer, [byte]32, 0, $end)
        $second = if ($first -ge 0) { [array]::IndexOf($buffer, [byte]32, $first + 1, $end - $first - 1) } else { -1 }
        if ($first -lt 0 -or $second -lt 0) {
            return $out
        }
        $question = [array]::IndexOf($buffer, [byte]63, $first + 1, $second - $first - 1)
        $pathEnd = if ($question -ge 0) { $question } else { $second }
        $out.Path = [System.Text.Encoding]::ASCII.GetString($buffer, $first + 1, $pathEnd - $first - 1)
        if ($question -ge 0) {
            $i = $question + 1
            while ($i -lt $second) {
                $amp = [array]::IndexOf($buffer, [byte]38, $i, $second - $i)
                if ($amp -lt 0) { $amp = $second }
                $eq = [array]::IndexOf($buffer, [byte]61, $i, $amp - $i)
                if ($eq -ge 0) {
                    $name = [System.Text.Encoding]::ASCII.GetString($buffer, $i, $eq - $i)
                    if ($name -eq 'code') {
                        # The code goes straight into a SecureString.
                        $secure = [securestring]::new()
                        for ($k = $eq + 1; $k -lt $amp; $k++) {
                            $b = $buffer[$k]
                            if ($b -eq 37 -and $k + 2 -lt $amp) {
                                $b = [System.Convert]::ToByte([System.Text.Encoding]::ASCII.GetString($buffer, $k + 1, 2), 16)
                                $k += 2
                            }
                            $secure.AppendChar([char]$b)
                        }
                        $secure.MakeReadOnly()
                        $out.Code = $secure
                    } elseif ($name -eq 'state') {
                        $out.State = [uri]::UnescapeDataString([System.Text.Encoding]::ASCII.GetString($buffer, $eq + 1, $amp - $eq - 1))
                    } elseif ($name -eq 'error') {
                        $out.Error = [uri]::UnescapeDataString([System.Text.Encoding]::ASCII.GetString($buffer, $eq + 1, $amp - $eq - 1).Replace('+', ' '))
                    }
                }
                $i = $amp + 1
            }
        }
        $ok = $null -ne $out.Code
        $title = if ($ok) { 'Signed in' } else { 'Not signed in' }
        $text = if ($ok) { 'You''re signed in to PagerArchive PowerShell. You can close this tab.' } else { 'PagerArchive PowerShell didn''t sign in. Go back to PowerShell to see why.' }
        $html = "<!doctype html><html lang=""en""><head><meta charset=""utf-8""><title>$title</title></head><body style=""font-family:system-ui,sans-serif;margin:3rem""><p>$text</p></body></html>"
        $status = if ($out.Path -eq '/') { '200 OK' } else { '404 Not Found' }
        $answer = [System.Text.Encoding]::UTF8.GetBytes("HTTP/1.1 $status`r`nContent-Type: text/html; charset=utf-8`r`nCache-Control: no-store`r`nConnection: close`r`nContent-Length: $([System.Text.Encoding]::UTF8.GetByteCount($html))`r`n`r`n$html")
        $stream.Write($answer, 0, $answer.Length)
        $stream.Flush()
        $out
    } finally {
        [array]::Clear($buffer)
        $stream.Dispose()
        $Client.Dispose()
    }
}

function Invoke-PaBrowserSignIn {
    <#
    .SYNOPSIS
    Signs in with the browser: PKCE and a loopback redirect on a free port.

    .DESCRIPTION
    Returns the token reply. Waits up to 5 minutes, in short polls, so
    Ctrl+C works.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The server's settings (Get-PaServerSetting).
        [Parameter(Mandatory)]
        [hashtable] $Settings
    )
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
        $redirect = "http://127.0.0.1:$($listener.LocalEndpoint.Port)/"
        $verifier = New-PaRandomString -Bytes 32
        $state = New-PaRandomString -Bytes 16
        $query = [ordered]@{
            response_type         = 'code'
            client_id             = $Settings.ClientId
            redirect_uri          = $redirect
            scope                 = $Settings.Scope
            state                 = $state
            code_challenge        = Get-PaCodeChallenge -Verifier $verifier
            code_challenge_method = 'S256'
        }
        $authorize = $Settings.Endpoints.Authorize + (ConvertTo-PaQueryString -Query $query)
        try {
            Start-PaBrowser -Uri $authorize -WhatIf:$false -Confirm:$false
        } catch {
            throw (New-PaErrorRecord -ErrorId 'no_browser' -Category ResourceUnavailable -Message "The browser couldn't be opened ($($_.Exception.Message)). Sign in with a code instead: Connect-PagerArchive -UseDeviceCode.")
        }
        Write-Verbose -Message "Waiting for the browser sign-in at $redirect"
        $deadline = [datetime]::UtcNow.AddMinutes(5)
        while ($true) {
            while (-not $listener.Pending()) {
                if ([datetime]::UtcNow -gt $deadline) {
                    throw (New-PaErrorRecord -ErrorId 'sign_in_timed_out' -Category OperationTimeout -Message 'The browser sign-in didn''t finish within 5 minutes. Run Connect-PagerArchive again, or use -UseDeviceCode.')
                }
                Start-Sleep -Milliseconds 200
            }
            $result = Read-PaLoopbackRequest -Client $listener.AcceptTcpClient()
            if ($result.Path -ne '/') {
                continue  # e.g. /favicon.ico
            }
            break
        }
        if ($result.State -ne $state) {
            throw (New-PaErrorRecord -ErrorId 'sign_in_state' -Category SecurityError -Message 'The sign-in came back without this sign-in''s state, so it was refused. Run Connect-PagerArchive again.')
        }
        if (-not $result.Code) {
            $why = if ($result.Error) { " ($($result.Error))" } else { '' }
            throw (New-PaErrorRecord -ErrorId 'sign_in_refused' -Category AuthenticationError -Message "Authentik didn't sign you in$why.")
        }
        $form = [ordered]@{
            grant_type    = 'authorization_code'
            code          = $result.Code
            redirect_uri  = $redirect
            client_id     = $Settings.ClientId
            code_verifier = $verifier
        }
        $reply = Invoke-PaTokenRequest -Uri $Settings.Endpoints.Token -Form $form
        $result.Code.Dispose()
        if ($reply.Status -ne 200 -or -not $reply['access_token']) {
            throw (New-PaErrorRecord -ErrorId 'sign_in_refused' -Status $reply.Status -Category AuthenticationError -Message "Authentik didn't sign you in ($($reply['error'])).")
        }
        $reply
    } finally {
        $listener.Stop()
    }
}

function Invoke-PaDeviceSignIn {
    <#
    .SYNOPSIS
    Signs in with a device code entered on another device (RFC 8628).
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The server's settings (Get-PaServerSetting).
        [Parameter(Mandatory)]
        [hashtable] $Settings
    )
    $device = Invoke-PaTokenRequest -Uri $Settings.Endpoints.Device -Form ([ordered]@{ client_id = $Settings.ClientId; scope = $Settings.Scope })
    if ($device.Status -ne 200 -or -not $device['device_code']) {
        throw (New-PaErrorRecord -ErrorId 'device_code_refused' -Status $device.Status -Category AuthenticationError -Message "Authentik didn't give a device code ($($device['error'])).")
    }
    $verification = if ($device['verification_uri']) { $device['verification_uri'] } else { $device['verification_uri_complete'] }
    if (([uri]$verification).GetLeftPart([System.UriPartial]::Authority) -ne ([uri]$Settings.Issuer).GetLeftPart([System.UriPartial]::Authority)) {
        throw (New-PaErrorRecord -ErrorId 'authentik_misconfigured' -Category InvalidData -Message 'Authentik''s device page isn''t on Authentik''s own address, so the module won''t show it.')
    }
    Show-PaDeviceCode -VerificationUri $verification -UserCode $device['user_code']
    $interval = if ($device['interval']) { [int]$device['interval'] } else { 5 }
    $deadline = [datetime]::UtcNow.AddSeconds($(if ($device['expires_in']) { [int]$device['expires_in'] } else { 600 }))
    try {
        while ([datetime]::UtcNow -lt $deadline) {
            Start-Sleep -Seconds $interval
            $form = [ordered]@{
                grant_type  = 'urn:ietf:params:oauth:grant-type:device_code'
                device_code = $device['device_code']
                client_id   = $Settings.ClientId
            }
            $reply = Invoke-PaTokenRequest -Uri $Settings.Endpoints.Token -Form $form
            if ($reply.Status -eq 200 -and $reply['access_token']) {
                return $reply
            }
            switch ($reply['error']) {
                'authorization_pending' { continue }
                'slow_down' { $interval += 5; continue }
                'access_denied' { throw (New-PaErrorRecord -ErrorId 'sign_in_refused' -Category AuthenticationError -Message 'The sign-in was refused on the other device.') }
                default { throw (New-PaErrorRecord -ErrorId 'sign_in_refused' -Status $reply.Status -Category AuthenticationError -Message "Authentik didn't sign you in ($($reply['error'])).") }
            }
        }
        throw (New-PaErrorRecord -ErrorId 'sign_in_timed_out' -Category OperationTimeout -Message 'The code expired before it was entered. Run Connect-PagerArchive -UseDeviceCode again.')
    } finally {
        $device['device_code'].Dispose()
    }
}

function Revoke-PaToken {
    <#
    .SYNOPSIS
    Revokes a token in Authentik (RFC 7009). Returns whether it worked.
    #>
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        # The connection or settings: Endpoints and ClientId.
        [Parameter(Mandatory)]
        [hashtable] $Settings,

        # The token.
        [Parameter(Mandatory)]
        [securestring] $Token,

        # refresh_token or access_token.
        [string] $Hint = 'refresh_token'
    )
    try {
        $reply = Invoke-PaTokenRequest -Uri $Settings.Endpoints.Revocation -Form ([ordered]@{ token = $Token; token_type_hint = $Hint; client_id = $Settings.ClientId })
        return $reply.Status -eq 200
    } catch {
        return $false
    }
}
