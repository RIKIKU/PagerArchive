function Connect-PagerArchive {
    <#
    .SYNOPSIS
    Signs in to a PagerArchive server.

    .DESCRIPTION
    Opens your browser at PagerArchive's normal sign-in (email and
    password). When you've signed in, the browser says you can close it,
    and the module is connected. On a machine without a browser, use
    -UseDeviceCode: the module shows a code to enter on any device.

    The sign-in is saved, so new PowerShell windows use it without signing
    in again:

    - On Windows, in a file in your profile, encrypted with Windows' data
      protection (DPAPI), so only your Windows account on this machine can
      read it. Nothing needs installing.
    - On Linux and macOS, in your default SecretManagement vault. With no
      vault registered, the sign-in lasts for this session only, and a
      warning says how to set one up.
    - Anywhere, -Vault names a SecretManagement vault to use instead.

    The module renews its access automatically. A sign-in that isn't used
    for 30 days ends. If one is already saved for the server, it's used
    without opening the browser.

    PowerShell access is for admins (R62): for anyone else the sign-in is
    refused and not saved.

    A tool connects with -ServiceAccount instead: a read-only service
    account (R62a), made on the server with `pagerarchive service-account
    create`. Its credential is saved the same way, and traded for a new
    access token whenever one is needed. Read commands work; a command that
    changes something is refused with the reason. The credential expires
    after 90 days, and ends at once when it's rotated or revoked on the
    server.

    .EXAMPLE
    Connect-PagerArchive -Uri https://pager.example.org

    Uri                       Name         Email                 Role  SavedIn
    ---                       ----         -----                 ----  -------
    https://pager.example.org Alex Example alex@example.org      admin DPAPI file

    .EXAMPLE
    Connect-PagerArchive -Uri https://pager.example.org -UseDeviceCode

    WARNING: To sign in, open https://auth.example.org/device on any device and enter the code ABCD-EFGH

    .EXAMPLE
    Connect-PagerArchive -Uri https://pager.example.org -ServiceAccount (Get-Credential -UserName report-bot)

    Connects as the service account report-bot: paste its credential as the password.

    .EXAMPLE
    Connect-PagerArchive -Uri http://192.168.1.20:8095 -AllowUnencryptedAuthentication

    Signs in to a test copy on your own network over plain HTTP, with a warning.

    .INPUTS
    System.Uri. Objects with a Uri property, such as a PagerArchive.Connection.

    .OUTPUTS
    PagerArchive.Connection. The server (Uri), who you're signed in as
    (Name, Email, Role), where the sign-in is saved (SavedIn) and when the
    current access token expires (ExpiresAt).

    .LINK
    Disconnect-PagerArchive

    .LINK
    about_PagerArchive
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding(DefaultParameterSetName = 'Browser')]
    [OutputType('PagerArchive.Connection')]
    param(
        # The PagerArchive app's URL, e.g. https://pager.example.org. No
        # default; no wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
        [uri] $Uri,

        # Sign in with a code entered on any device, for a machine with no
        # browser. Off by default.
        [Parameter(ParameterSetName = 'Browser')]
        [switch] $UseDeviceCode,

        # Connect as a read-only service account (R62a) instead of signing
        # in: its name as the user name, and the credential that
        # `pagerarchive service-account create` printed on the server as the
        # password. No browser. It can read what an admin reads, except
        # people's personal details, and change nothing.
        [Parameter(Mandatory, ParameterSetName = 'ServiceAccount')]
        [System.Management.Automation.Credential()]
        [pscredential] $ServiceAccount,

        # The SecretManagement vault to save the sign-in in. Default: a
        # DPAPI-encrypted file on Windows, the default vault on Linux and
        # macOS. Needs the Microsoft.PowerShell.SecretManagement module. No
        # wildcards.
        [ValidateNotNullOrEmpty()]
        [string] $Vault,

        # Allow a plain-HTTP URL (a test copy on your own network), which
        # sends the sign-in unencrypted. Off by default.
        [switch] $AllowUnencryptedAuthentication
    )
    process {
        try {
            $origin = Get-PaOrigin -Uri $Uri -AllowUnencryptedAuthentication:$AllowUnencryptedAuthentication
            if ($origin.StartsWith('http:')) {
                Write-Warning -Message "$origin uses plain HTTP: your sign-in is sent unencrypted."
            }
            if ($PSBoundParameters.ContainsKey('ServiceAccount')) {
                return Connect-PaServiceAccount -Origin $origin -Credential $ServiceAccount -Vault $Vault -AllowUnencryptedAuthentication:$AllowUnencryptedAuthentication
            }
            $settings = Get-PaServerSetting -Origin $origin -AllowUnencryptedAuthentication:$AllowUnencryptedAuthentication
            $store = Resolve-PaStore -Vault $Vault

            # A saved sign-in for this server: use it, without the browser.
            $saved = Read-PaSignIn -Origin $origin
            if ($saved -and (-not $Vault -or ($saved.store -eq 'vault' -and $saved.vault -eq $Vault))) {
                try {
                    $connection = Open-PaSavedSignIn -Record $saved
                    $connection.User = Invoke-PaApi -Method GET -Path '/api/v1/me'
                    return New-PaConnectionObject -Connection $connection
                } catch {
                    $script:Connection = $null
                    # Refused for who you are: signing in again won't help.
                    if ($_.Exception.Data['PagerArchive.Code'] -in @('powershell_admins_only', 'deactivated', 'not_signed_in', 'service_account_expired', 'service_account_refused')) {
                        throw
                    }
                    Write-Verbose -Message "The saved sign-in didn't work ($($_.Exception.Data['PagerArchive.Code'])); signing in again."
                }
            }
            if ($saved) {
                Remove-PaSignIn -Origin $origin -WhatIf:$false -Confirm:$false
            }

            $reply = if ($UseDeviceCode) { Invoke-PaDeviceSignIn -Settings $settings } else { Invoke-PaBrowserSignIn -Settings $settings }
            $connection = @{
                Uri              = $origin
                Issuer           = $settings.Issuer
                ClientId         = $settings.ClientId
                Scope            = $settings.Scope
                Endpoints        = $settings.Endpoints
                AccessToken      = $null
                ExpiresAt        = [datetimeoffset]::MinValue
                RefreshToken     = $null
                Store            = $store.Store
                Vault            = $store.Vault
                Version          = $null
                AllowUnencrypted = [bool]$AllowUnencryptedAuthentication
                User             = $null
            }
            Import-PaTokenReply -Connection $connection -Reply $reply
            if ($reply['id_token']) {
                $reply['id_token'].Dispose()
            }

            # The API decides: only active admins (R62).
            $script:Connection = $connection
            try {
                $connection.User = Invoke-PaApi -Method GET -Path '/api/v1/me'
            } catch {
                $script:Connection = $null
                $null = Revoke-PaToken -Settings $connection -Token $connection.RefreshToken
                throw
            }
            if ($connection.Store -eq 'session') {
                Write-Warning -Message ('No SecretManagement vault is registered, so this sign-in lasts until this PowerShell session ends. ' +
                    'To stay signed in, register a vault: Install-PSResource -Name Microsoft.PowerShell.SecretManagement, Microsoft.PowerShell.SecretStore; ' +
                    'Register-SecretVault -Name SecretStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault')
            } else {
                Save-PaSignIn -Connection $connection
                $connection.RefreshToken = $null
            }
            New-PaConnectionObject -Connection $connection
        } catch {
            $script:Connection = $null
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Uri))
        }
    }
}

