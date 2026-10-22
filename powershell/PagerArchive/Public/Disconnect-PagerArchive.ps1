function Disconnect-PagerArchive {
    <#
    .SYNOPSIS
    Signs out of a PagerArchive server.

    .DESCRIPTION
    Revokes the sign-in in Authentik, so it can't be renewed, and deletes
    the saved copy, wherever it's saved (the DPAPI file on Windows, or the
    vault). The next command asks you to run Connect-PagerArchive.

    If Authentik can't be reached, the saved copy is still deleted, and a
    warning says the sign-in couldn't be revoked: it ends by itself after
    30 days unused.

    .EXAMPLE
    Disconnect-PagerArchive

    Signs out of the server this session is connected to.

    .EXAMPLE
    Disconnect-PagerArchive -Uri https://pager.example.org

    Signs out of that server's saved sign-in, from any window.

    .INPUTS
    System.Uri. Objects with a Uri property, such as a PagerArchive.Connection.

    .OUTPUTS
    None.

    .LINK
    Connect-PagerArchive

    .LINK
    about_PagerArchive
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingAllowUnencryptedAuthentication', '', Justification = 'Only when the user passes -AllowUnencryptedAuthentication for a LAN test copy, with a warning (guidelines section 9).')]
    [CmdletBinding()]
    [OutputType([void])]
    param(
        # The server to sign out of. Default: the one this session is
        # connected to, or the only saved sign-in. No wildcards.
        [Parameter(Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
        [uri] $Uri
    )
    process {
        try {
            $connection = $null
            if ($Uri) {
                $origin = $Uri.GetLeftPart([System.UriPartial]::Authority)
                if ($script:Connection -and $script:Connection.Uri -eq $origin) {
                    $connection = $script:Connection
                }
            } else {
                $connection = $script:Connection
                if (-not $connection) {
                    $saved = @(Read-PaSignIn)
                    if ($saved.Count -eq 0) {
                        Write-Warning -Message 'Not signed in to PagerArchive.'
                        return
                    }
                    if ($saved.Count -gt 1) {
                        throw (New-PaErrorRecord -ErrorId 'several_servers' -Category InvalidArgument -Message "You're signed in to several PagerArchive servers ($(($saved | ForEach-Object -Process { $_.uri }) -join ', ')). Say which with -Uri.")
                    }
                    $origin = $saved[0].uri
                } else {
                    $origin = $connection.Uri
                }
            }
            $saved = Read-PaSignIn -Origin $origin

            # The refresh token: this session's, or the saved one.
            $refresh = $null
            if ($connection -and $connection.Store -eq 'session') {
                $refresh = $connection.RefreshToken
            } elseif ($saved) {
                try {
                    $refresh = (Read-PaRefreshToken -Origin $origin).RefreshToken
                } catch {
                    Write-Warning -Message "The saved sign-in couldn't be read, so it can't be revoked: $($_.Exception.Message)"
                }
            }
            if (-not $refresh -and -not $connection) {
                Write-Warning -Message "Not signed in to $origin."
                return
            }
            $revoked = $false
            if ($refresh) {
                try {
                    $settings = if ($connection) { $connection } else { Get-PaServerSetting -Origin $origin -AllowUnencryptedAuthentication:([bool]$saved.allowUnencryptedAuthentication) }
                    $revoked = Revoke-PaToken -Settings $settings -Token $refresh
                    if ($connection -and $connection.AccessToken) {
                        $null = Revoke-PaToken -Settings $settings -Token $connection.AccessToken -Hint 'access_token'
                    }
                } catch {
                    $revoked = $false
                }
            }
            Remove-PaSignIn -Origin $origin -WhatIf:$false -Confirm:$false
            if ($script:Connection -and $script:Connection.Uri -eq $origin) {
                $script:Connection = $null
            }
            if (-not $revoked) {
                Write-Warning -Message "Signed out here, but the sign-in couldn't be revoked in Authentik. It ends by itself after 30 days unused."
            }
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Uri))
        }
    }
}
