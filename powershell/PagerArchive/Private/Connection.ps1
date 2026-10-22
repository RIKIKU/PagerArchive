# The session's connection and every call to PagerArchive's API.
#
# $script:Connection holds: Uri (the app's origin), Issuer, ClientId, Scope,
# the Authentik endpoints, AccessToken and ExpiresAt, RefreshToken (kept in
# memory only for a session-only sign-in, and between saves), Store, Vault,
# Version (of the saved copy) and AllowUnencrypted.

function Get-PaOrigin {
    <#
    .SYNOPSIS
    The origin of a URL (scheme, host and port), checking its scheme.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # The URL.
        [Parameter(Mandatory)]
        [uri] $Uri,

        # Allow http://, with a warning.
        [switch] $AllowUnencryptedAuthentication,

        # What the URL is, for messages.
        [string] $What = 'PagerArchive'
    )
    if (-not $Uri.IsAbsoluteUri -or $Uri.Scheme -notin @('http', 'https')) {
        throw (New-PaErrorRecord -ErrorId 'bad_uri' -Category InvalidArgument -Message "Give $What's URL in full, like https://pager.example.org.")
    }
    if ($Uri.Scheme -eq 'http' -and -not $AllowUnencryptedAuthentication) {
        throw (New-PaErrorRecord -ErrorId 'unencrypted_refused' -Category SecurityError `
                -Message "$What's URL ($($Uri.GetLeftPart([System.UriPartial]::Authority))) uses plain HTTP, which would send your sign-in unencrypted. Use its https:// URL, or for a test copy on your own network add -AllowUnencryptedAuthentication.")
    }
    $Uri.GetLeftPart([System.UriPartial]::Authority)
}

function Get-PaServerSetting {
    <#
    .SYNOPSIS
    Reads what the module needs from the app's URL alone (R60), and the issuer's endpoints.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The app's origin.
        [Parameter(Mandatory)]
        [string] $Origin,

        # Allow http://.
        [switch] $AllowUnencryptedAuthentication
    )
    try {
        Write-Verbose -Message 'GET /auth/powershell'
        $settings = Invoke-RestMethod -Uri "$Origin/auth/powershell" -Method Get -TimeoutSec 30 -Verbose:$false -ErrorAction Stop
    } catch {
        throw (New-PaErrorRecord -ErrorId 'server_unreachable' -Category ConnectionError -Message "PagerArchive can't be reached at ${Origin}, or it doesn't offer PowerShell sign-in: $($_.Exception.Message)")
    }
    $issuerOrigin = Get-PaOrigin -Uri $settings.issuer -AllowUnencryptedAuthentication:$AllowUnencryptedAuthentication -What 'Authentik'
    try {
        Write-Verbose -Message 'GET /.well-known/openid-configuration (Authentik)'
        $discovery = Invoke-RestMethod -Uri ($settings.issuer.TrimEnd('/') + '/.well-known/openid-configuration') -Method Get -TimeoutSec 30 -Verbose:$false -ErrorAction Stop
    } catch {
        throw (New-PaErrorRecord -ErrorId 'authentik_unreachable' -Category ConnectionError -Message "Authentik can't be reached at ${issuerOrigin}: $($_.Exception.Message)")
    }
    $endpoints = @{
        Authorize  = $discovery.authorization_endpoint
        Token      = $discovery.token_endpoint
        Revocation = $discovery.revocation_endpoint
        Device     = $discovery.device_authorization_endpoint
    }
    # The module only ever talks to PagerArchive and its Authentik.
    foreach ($name in @($endpoints.Keys)) {
        if (-not $endpoints[$name] -or ([uri]$endpoints[$name]).GetLeftPart([System.UriPartial]::Authority) -ne $issuerOrigin) {
            throw (New-PaErrorRecord -ErrorId 'authentik_misconfigured' -Category InvalidData -Message "Authentik's $name endpoint isn't at $issuerOrigin, so the module won't use it.")
        }
    }
    @{
        Issuer    = $settings.issuer
        ClientId  = $settings.client_id
        Scope     = $settings.scope
        Endpoints = $endpoints
    }
}

function Get-PaConnection {
    <#
    .SYNOPSIS
    The session's connection, or the saved sign-in when this session has none.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param()
    if ($script:Connection) {
        return $script:Connection
    }
    $saved = @(Read-PaSignIn)
    if ($saved.Count -eq 0) {
        throw (New-PaErrorRecord -ErrorId 'not_connected' -Category AuthenticationError -Message 'Not signed in to PagerArchive. Run Connect-PagerArchive -Uri <the app''s URL>.')
    }
    if ($saved.Count -gt 1) {
        $list = ($saved | ForEach-Object -Process { $_.uri }) -join ', '
        throw (New-PaErrorRecord -ErrorId 'several_servers' -Category InvalidOperation -Message "You're signed in to several PagerArchive servers ($list). Choose one with Connect-PagerArchive -Uri <URL>.")
    }
    Open-PaSavedSignIn -Record $saved[0]
}

function Open-PaSavedSignIn {
    <#
    .SYNOPSIS
    Makes a saved sign-in this session's connection, renewing its access token.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The saved sign-in's record (Read-PaSignIn).
        [Parameter(Mandatory)]
        [hashtable] $Record
    )
    $allow = [bool]$Record.allowUnencryptedAuthentication
    $origin = Get-PaOrigin -Uri $Record.uri -AllowUnencryptedAuthentication:$allow
    if ($allow -and $origin.StartsWith('http:')) {
        Write-Warning -Message "Using your saved sign-in to $origin over plain HTTP, unencrypted."
    }
    $settings = Get-PaServerSetting -Origin $origin -AllowUnencryptedAuthentication:$allow
    $connection = @{
        Uri              = $origin
        Issuer           = $settings.Issuer
        ClientId         = $settings.ClientId
        Scope            = $settings.Scope
        Endpoints        = $settings.Endpoints
        AccessToken      = $null
        ExpiresAt        = [datetimeoffset]::MinValue
        RefreshToken     = $null
        Store            = $Record.store
        Vault            = $Record.vault
        Version          = $null
        AllowUnencrypted = $allow
        User             = $null
    }
    Request-PaToken -Connection $connection
    $script:Connection = $connection
    $connection
}

function Request-PaToken {
    <#
    .SYNOPSIS
    Renews a connection's access token with its refresh token.

    .DESCRIPTION
    Authentik issues a new refresh token each time and revokes the old one
    (ADR 0009 section 4), so the new one is saved at once. Several sessions
    share one saved sign-in: the saved token is read first, and if Authentik
    refuses it because another session renewed meanwhile, the newer one is
    read and tried once more. A sign-in Authentik refuses has ended: the
    saved copy is deleted.
    #>
    [CmdletBinding()]
    param(
        # The connection to renew.
        [Parameter(Mandatory)]
        [hashtable] $Connection
    )
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        if ($Connection.Store -ne 'session') {
            $saved = Read-PaRefreshToken -Origin $Connection.Uri
            if (-not $saved) {
                throw (New-PaErrorRecord -ErrorId 'sign_in_ended' -Category AuthenticationError -Message 'Your PagerArchive sign-in has ended. Run Connect-PagerArchive.')
            }
            $Connection.RefreshToken = $saved.RefreshToken
            $Connection.Version = $saved.Version
        }
        $form = [ordered]@{
            grant_type    = 'refresh_token'
            refresh_token = $Connection.RefreshToken
            client_id     = $Connection.ClientId
        }
        $reply = Invoke-PaTokenRequest -Uri $Connection.Endpoints.Token -Form $form
        if ($reply.Status -eq 200 -and $reply['access_token']) {
            Import-PaTokenReply -Connection $Connection -Reply $reply
            Save-PaSignIn -Connection $Connection
            return
        }
        if ($reply['error'] -ne 'invalid_grant') {
            throw (New-PaErrorRecord -ErrorId 'renewal_failed' -Status $reply.Status -Message "Authentik didn't renew your sign-in ($($reply['error']))." -Category AuthenticationError)
        }
        # Another session may have renewed it just now: try the newer one.
        if ($Connection.Store -ne 'session') {
            Start-Sleep -Milliseconds 300
            $now = Read-PaSignIn -Origin $Connection.Uri
            if ($now -and $now.version -ne $Connection.Version) {
                continue
            }
            Remove-PaSignIn -Origin $Connection.Uri -WhatIf:$false -Confirm:$false
        }
        break
    }
    if ($script:Connection -and $script:Connection.Uri -eq $Connection.Uri) {
        $script:Connection = $null
    }
    throw (New-PaErrorRecord -ErrorId 'sign_in_ended' -Category AuthenticationError -Message 'Your PagerArchive sign-in has ended. Run Connect-PagerArchive.')
}

function Import-PaTokenReply {
    <#
    .SYNOPSIS
    Takes the tokens from a token reply into a connection.
    #>
    [CmdletBinding()]
    param(
        # The connection.
        [Parameter(Mandatory)]
        [hashtable] $Connection,

        # The token endpoint's reply.
        [Parameter(Mandatory)]
        [hashtable] $Reply
    )
    $Connection.AccessToken = $Reply['access_token']
    $Connection.ExpiresAt = [datetimeoffset]::UtcNow.AddSeconds([double]$Reply['expires_in'])
    if ($Reply['refresh_token']) {
        $Connection.RefreshToken = $Reply['refresh_token']
    }
}

function ConvertTo-PaQueryString {
    <#
    .SYNOPSIS
    Builds a query string from names and values; arrays repeat the name.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # Names and values.
        [System.Collections.IDictionary] $Query
    )
    if (-not $Query -or $Query.Count -eq 0) {
        return ''
    }
    $parts = foreach ($key in $Query.Keys) {
        foreach ($value in @($Query[$key])) {
            if ($null -ne $value -and "$value" -ne '') {
                [uri]::EscapeDataString($key) + '=' + [uri]::EscapeDataString([string]$value)
            }
        }
    }
    if (-not $parts) {
        return ''
    }
    '?' + ($parts -join '&')
}

function Invoke-PaApi {
    <#
    .SYNOPSIS
    Calls PagerArchive's API as the signed-in admin.

    .DESCRIPTION
    Renews the access token when it has under 60 seconds left. A refusal
    becomes an ErrorRecord (guidelines section 7), thrown for the caller to
    catch. HTTP errors don't throw inside Invoke-RestMethod
    (-SkipHttpErrorCheck), so no exception holding the request, and its
    Authorization header, ever reaches $Error. -Verbose shows the method,
    the path and the query's names only, because values could hold page
    text (P1).
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding()]
    param(
        # GET, POST, PUT, PATCH or DELETE.
        [Parameter(Mandatory)]
        [string] $Method,

        # The path, e.g. /api/v1/admin/templates.
        [Parameter(Mandatory)]
        [string] $Path,

        # Query parameters; arrays repeat the name.
        [System.Collections.IDictionary] $Query,

        # A body, sent as JSON.
        [object] $Body,

        # A body sent as it is, as application/octet-stream (an icon file).
        [byte[]] $RawBody,

        # Statuses that aren't errors, besides 2xx (e.g. 409 for the import).
        [int[]] $Accept = @()
    )
    $connection = Get-PaConnection
    for ($try = 1; $try -le 2; $try++) {
        if ($connection.ExpiresAt -lt [datetimeoffset]::UtcNow.AddSeconds(60)) {
            Request-PaToken -Connection $connection
        }
        $names = @(if ($Query) { $Query.Keys | Where-Object -FilterScript { $null -ne $Query[$_] -and "$($Query[$_])" -ne '' } })
        $shown = if ($names.Count) { " ($($names -join ', '))" } else { '' }
        Write-Verbose -Message "$Method $Path$shown"
        $request = @{
            Uri                            = $connection.Uri + $Path + (ConvertTo-PaQueryString -Query $Query)
            Method                         = $Method
            Authentication                 = 'Bearer'
            Token                          = $connection.AccessToken
            AllowUnencryptedAuthentication = [bool]$connection.AllowUnencrypted
            SkipHttpErrorCheck             = $true
            StatusCodeVariable             = 'status'
            TimeoutSec                     = 120
            Verbose                        = $false
            Debug                          = $false
            ErrorAction                    = 'Stop'
        }
        if ($PSBoundParameters.ContainsKey('Body')) {
            $request.Body = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 8 -Compress))
            $request.ContentType = 'application/json; charset=utf-8'
        }
        if ($PSBoundParameters.ContainsKey('RawBody')) {
            $request.Body = $RawBody
            $request.ContentType = 'application/octet-stream'
        }
        try {
            $reply = Invoke-RestMethod @request
        } catch {
            throw (New-PaErrorRecord -ErrorId 'server_unreachable' -Category ConnectionError -Message "PagerArchive can't be reached at $($connection.Uri): $($_.Exception.GetBaseException().Message)")
        }
        Write-Debug -Message "$Method $Path answered $status"
        if ($status -lt 300 -or $status -in $Accept) {
            return $reply
        }
        $code = $null
        $message = $null
        if ($reply -is [psobject] -and $reply.PSObject.Properties['error']) {
            $message = $reply.error
            if ($reply.PSObject.Properties['code']) {
                $code = $reply.code
            }
        }
        # An access token Authentik has since refused, or one that expired
        # early: renew once and try again.
        if ($status -eq 401 -and $code -eq 'invalid_token' -and $try -eq 1) {
            $connection.ExpiresAt = [datetimeoffset]::MinValue
            continue
        }
        if (-not $code) {
            $code = "http_$status"
        }
        if (-not $message) {
            $message = "PagerArchive answered $status."
        }
        throw (New-PaErrorRecord -ErrorId $code -Status $status -Message $message)
    }
}
