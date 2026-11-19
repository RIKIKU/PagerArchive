# Service accounts (R62a, ADR 0048): a read-only, non-person account for a
# tool. Its credential (an Authentik app password, from `pagerarchive
# service-account create` on the server) is traded for a 5-minute access
# token with OAuth's client-credentials grant, whenever one is needed. It's
# saved where a person's refresh token would be (R61), and never shown or
# logged.

function Connect-PaServiceAccount {
    <#
    .SYNOPSIS
    Connects as a service account, saving its credential.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding()]
    [OutputType('PagerArchive.Connection')]
    param(
        # The app's origin.
        [Parameter(Mandatory)]
        [string] $Origin,

        # The service account's name and credential.
        [Parameter(Mandatory)]
        [pscredential] $Credential,

        # The vault to save it in, if not the default.
        [string] $Vault,

        # Allow http://.
        [switch] $AllowUnencryptedAuthentication
    )
    $name = $Credential.UserName.TrimStart('\')
    if ($name -cnotmatch '^[a-z0-9][a-z0-9-]{2,39}$') {
        throw (New-PaErrorRecord -ErrorId 'bad_service_account' -Category InvalidArgument -Message "'$name' isn't a service account's name: 3 to 40 lower-case letters, digits and hyphens, as pagerarchive service-account list shows it.")
    }
    if ($Credential.Password.Length -eq 0) {
        throw (New-PaErrorRecord -ErrorId 'bad_service_account' -Category InvalidArgument -Message 'Give the service account''s credential as the password.')
    }
    $settings = Get-PaServerSetting -Origin $Origin -AllowUnencryptedAuthentication:$AllowUnencryptedAuthentication -ServiceAccount
    $store = Resolve-PaStore -Vault $Vault
    $connection = @{
        Uri              = $Origin
        Issuer           = $settings.Issuer
        ClientId         = $settings.ClientId
        Scope            = $settings.Scope
        Endpoints        = $settings.Endpoints
        AccessToken      = $null
        ExpiresAt        = [datetimeoffset]::MinValue
        RefreshToken     = $null
        ServiceAccount   = $name
        Secret           = $Credential.Password.Copy()
        Store            = $store.Store
        Vault            = $store.Vault
        Version          = $null
        AllowUnencrypted = [bool]$AllowUnencryptedAuthentication
        User             = $null
    }
    Request-PaServiceToken -Connection $connection
    $script:Connection = $connection
    try {
        $connection.User = Invoke-PaApi -Method GET -Path '/api/v1/me'
    } catch {
        $script:Connection = $null
        throw
    }
    if (-not $connection.User.service_account) {
        $script:Connection = $null
        throw (New-PaErrorRecord -ErrorId 'not_service_account' -Category AuthenticationError -Message "PagerArchive doesn't know $name as a service account.")
    }
    # The new credential replaces any sign-in saved for this server.
    Remove-PaSignIn -Origin $Origin -WhatIf:$false -Confirm:$false
    if ($connection.Store -eq 'session') {
        Write-Warning -Message ('No SecretManagement vault is registered, so this connection lasts until this PowerShell session ends. ' +
            'To stay connected, register a vault: Install-PSResource -Name Microsoft.PowerShell.SecretManagement, Microsoft.PowerShell.SecretStore; ' +
            'Register-SecretVault -Name SecretStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault')
    } else {
        Save-PaSignIn -Connection $connection
        $connection.Secret.Dispose()
        $connection.Secret = $null
    }
    New-PaConnectionObject -Connection $connection
}

function Request-PaServiceToken {
    <#
    .SYNOPSIS
    Gets a service account's connection a new access token with its credential.

    .DESCRIPTION
    The credential is the connection's own (just given, or this session's
    only), or the saved one. Authentik refusing it means it was rotated,
    revoked or has expired, or the account was deactivated: the saved copy
    is deleted, since it will never work again.
    #>
    [CmdletBinding()]
    param(
        # The connection.
        [Parameter(Mandatory)]
        [hashtable] $Connection
    )
    $secret = $Connection.Secret
    if (-not $secret) {
        $saved = Read-PaRefreshToken -Origin $Connection.Uri
        if (-not $saved) {
            throw (New-PaErrorRecord -ErrorId 'sign_in_ended' -Category AuthenticationError -Message "The service account $($Connection.ServiceAccount) isn't connected any more. Run Connect-PagerArchive -ServiceAccount.")
        }
        $secret = $saved.RefreshToken
    }
    $form = [ordered]@{
        grant_type = 'client_credentials'
        client_id  = $Connection.ClientId
        username   = $Connection.ServiceAccount
        password   = $secret
        scope      = $Connection.Scope
    }
    $reply = Invoke-PaTokenRequest -Uri $Connection.Endpoints.Token -Form $form
    if ($reply.Status -eq 200 -and $reply['access_token']) {
        Import-PaTokenReply -Connection $Connection -Reply $reply
        if ($reply['id_token']) {
            $reply['id_token'].Dispose()
        }
        return
    }
    if ($reply['error'] -ne 'invalid_grant') {
        throw (New-PaErrorRecord -ErrorId 'renewal_failed' -Status $reply.Status -Message "Authentik didn't give the service account a token ($($reply['error']))." -Category AuthenticationError)
    }
    if (-not $Connection.Secret) {
        Remove-PaSignIn -Origin $Connection.Uri -WhatIf:$false -Confirm:$false
    }
    if ($script:Connection -and $script:Connection.Uri -eq $Connection.Uri) {
        $script:Connection = $null
    }
    throw (New-PaErrorRecord -ErrorId 'service_account_refused' -Category AuthenticationError `
            -Message "Authentik refused the service account $($Connection.ServiceAccount)'s credential: it's wrong, or it was rotated or revoked, or it has expired, or an admin deactivated the account. Ask the server's operator (pagerarchive service-account list).")
}
