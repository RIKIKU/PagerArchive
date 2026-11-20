#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Service accounts (R62a, A130, ADR 0048): Connect-PagerArchive
# -ServiceAccount trades the credential for access tokens with the
# client-credentials grant, saves it as a sign-in is saved (R61), and shows
# the API's refusal of a write. Authentik and the API are replaced here; the
# stack tests connect for real.

BeforeAll {
    $manifest = Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1'
    Import-Module -Name $manifest -Force
    $origin = 'https://pager.example.test'
    $plain = 'sa-' + [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')

    function ConvertTo-Secure([string] $Text) {
        $s = [securestring]::new()
        foreach ($c in $Text.ToCharArray()) { $s.AppendChar($c) }
        $s.MakeReadOnly()
        $s
    }

    function New-Credential([string] $Name = 'report-bot', [string] $Secret = $plain) {
        [pscredential]::new($Name, (ConvertTo-Secure -Text $Secret))
    }

    # The text of a SecureString, for checking what was sent.
    function ConvertFrom-Secure([securestring] $Secure) {
        [System.Net.NetworkCredential]::new('', $Secure).Password
    }

    function Use-FakeServer {
        Mock -ModuleName PagerArchive Get-PaServerSetting {
            if ($ServiceAccount) {
                @{ Issuer = 'https://auth.example.test/application/o/pagerarchive-service/'; ClientId = 'pagerarchive-service'; Scope = 'openid profile'
                    Endpoints = @{ Authorize = 'https://auth.example.test/a'; Token = 'https://auth.example.test/t'; Revocation = 'https://auth.example.test/r'; Device = 'https://auth.example.test/d' }
                }
            } else {
                @{ Issuer = 'https://auth.example.test/application/o/pagerarchive-powershell/'; ClientId = 'pagerarchive-powershell'; Scope = 'openid'
                    Endpoints = @{ Authorize = 'https://auth.example.test/a'; Token = 'https://auth.example.test/t'; Revocation = 'https://auth.example.test/r'; Device = 'https://auth.example.test/d' }
                }
            }
        }
        # What each token request sent, its password as text (the module
        # disposes of its copy once it's saved).
        $script:sent = [System.Collections.Generic.List[hashtable]]::new()
        Mock -ModuleName PagerArchive Invoke-PaTokenRequest {
            $script:sent.Add(@{ grant_type = $Form.grant_type; client_id = $Form.client_id; username = $Form.username; secure = $Form.password -is [securestring]; password = (ConvertFrom-Secure $Form.password) })
            @{ Status = 200; access_token = (ConvertTo-Secure -Text 'access'); expires_in = 300 }
        } -ParameterFilter { $Uri -eq 'https://auth.example.test/t' }
        Mock -ModuleName PagerArchive Invoke-PaTokenRequest { @{ Status = 200 } } -ParameterFilter { $Uri -eq 'https://auth.example.test/r' }
        # As Invoke-RestMethod reads it: an ISO 8601 time becomes a DateTime.
        $script:credentialExpires = [datetime]::SpecifyKind([datetime]'2027-02-18T09:30:00', [System.DateTimeKind]::Utc)
        Mock -ModuleName PagerArchive Invoke-PaApi { [pscustomobject]@{ id = 7; email = ''; name = 'report-bot'; role = 'user'; service_account = $true; credential_expires_at = $script:credentialExpires } } -ParameterFilter { $Path -eq '/api/v1/me' }
    }
}

Describe 'Connect-PagerArchive -ServiceAccount (R62a)' {
    BeforeEach {
        $env:PAGERARCHIVE_SIGNIN_DIR = Join-Path -Path $TestDrive -ChildPath ('SignIns-' + [guid]::NewGuid().ToString('N'))
        & (Get-Module PagerArchive) { $script:Connection = $null }
        Use-FakeServer
    }

    AfterAll {
        Remove-Item -Path Env:PAGERARCHIVE_SIGNIN_DIR -ErrorAction SilentlyContinue
    }

    It 'trades the credential for a token with client credentials, as the service account' {
        $connection = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) -WarningAction SilentlyContinue
        $connection.Name | Should -Be 'report-bot'
        $connection.Role | Should -Be 'service account'
        $script:sent.Count | Should -Be 1
        $s = $script:sent[0]
        $s.grant_type | Should -Be 'client_credentials'
        $s.client_id | Should -Be 'pagerarchive-service'
        $s.username | Should -Be 'report-bot'
        $s.secure | Should -BeTrue
        $s.password | Should -Be $plain
    }

    It 'shows when the credential expires as ExpiresAt, not the 5-minute access token' {
        $connection = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) -WarningAction SilentlyContinue
        $connection.ExpiresAt | Should -BeOfType [datetimeoffset]
        $connection.ExpiresAt | Should -Be ([datetimeoffset]'2027-02-18T09:30:00Z')
        $connection.ExpiresAt.Offset | Should -Be ([timespan]::Zero)
    }

    It 'reads the credential''s expiry as text too' {
        Mock -ModuleName PagerArchive Invoke-PaApi { [pscustomobject]@{ id = 7; email = ''; name = 'report-bot'; role = 'user'; service_account = $true; credential_expires_at = '2027-02-18T09:30:00Z' } } -ParameterFilter { $Path -eq '/api/v1/me' }
        $connection = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) -WarningAction SilentlyContinue
        $connection.ExpiresAt | Should -Be ([datetimeoffset]'2027-02-18T09:30:00Z')
    }

    It 'shows no ExpiresAt when the server doesn''t say when the credential expires' {
        Mock -ModuleName PagerArchive Invoke-PaApi { [pscustomobject]@{ id = 7; email = ''; name = 'report-bot'; role = 'user'; service_account = $true } } -ParameterFilter { $Path -eq '/api/v1/me' }
        $connection = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) -WarningAction SilentlyContinue
        $connection.ExpiresAt | Should -BeNullOrEmpty
    }

    It 'refuses a name that isn''t a service account''s, before asking anyone' {
        { Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential -Name 'Someone@example.test') } |
            Should -Throw -ErrorId 'bad_service_account,Connect-PagerArchive'
        Should -Invoke -ModuleName PagerArchive Invoke-PaTokenRequest -Times 0 -Exactly
    }

    It 'can''t be combined with -UseDeviceCode' {
        { Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) -UseDeviceCode } | Should -Throw -ErrorId 'AmbiguousParameterSet,Connect-PagerArchive'
    }

    It 'says the credential was refused, and saves nothing' {
        Mock -ModuleName PagerArchive Invoke-PaTokenRequest { @{ Status = 400; error = 'invalid_grant' } } -ParameterFilter { $Uri -eq 'https://auth.example.test/t' }
        { Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) } | Should -Throw -ErrorId 'service_account_refused,Connect-PagerArchive'
        @(Get-ChildItem -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR -ErrorAction SilentlyContinue).Count | Should -Be 0
    }

    It 'shows the API''s refusal of a write, with its code' {
        $null = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential) -WarningAction SilentlyContinue
        # What Invoke-PaApi throws for the API's 403 (Module.Tests checks that it does).
        Mock -ModuleName PagerArchive Invoke-PaApi {
            throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'service_account_read_only' -Status 403 -Message $args[0] } 'This is a read-only service account: it can''t change anything in PagerArchive. Sign in as an admin to make changes.')
        } -ParameterFilter { $Method -eq 'POST' }
        $err = $null
        try {
            New-PagerArchiveTemplate -Name 'Made up' -Agency 'Made-up agency' -Confirm:$false -ErrorAction Stop
        } catch {
            $err = $_
        }
        $err | Should -Not -BeNullOrEmpty
        $err.FullyQualifiedErrorId | Should -BeLike 'service_account_read_only*'
        $err.Exception.Message | Should -BeLike '*read-only service account*'
    }

    Describe 'On Windows: saved in the DPAPI file' -Skip:(-not $IsWindows) {
        It 'saves the credential encrypted, and a new window connects with it' {
            $connection = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential)
            $connection.SavedIn | Should -Be 'DPAPI file'
            $files = @(Get-ChildItem -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR)
            $files.Count | Should -Be 1
            $text = Get-Content -LiteralPath $files[0].FullName -Raw
            $text.Contains($plain) | Should -BeFalse
            ($text | ConvertFrom-Json).serviceAccount | Should -Be 'report-bot'

            # A new session: the saved credential gets a new token.
            & (Get-Module PagerArchive) { $script:Connection = $null }
            $again = Connect-PagerArchive -Uri $origin
            $again.Role | Should -Be 'service account'
            $again.ExpiresAt | Should -Be ([datetimeoffset]'2027-02-18T09:30:00Z')
            @($script:sent | Where-Object { $_.grant_type -eq 'client_credentials' -and $_.password -eq $plain }).Count | Should -Be 2
        }

        It 'deletes the saved credential when Authentik refuses it later (rotated, revoked, expired)' {
            $null = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential)
            & (Get-Module PagerArchive) { $script:Connection = $null }
            Mock -ModuleName PagerArchive Invoke-PaTokenRequest { @{ Status = 400; error = 'invalid_grant' } } -ParameterFilter { $Uri -eq 'https://auth.example.test/t' }
            { Get-PagerArchiveTemplate -ErrorAction Stop } | Should -Throw -ErrorId 'service_account_refused*'
            @(Get-ChildItem -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR).Count | Should -Be 0
        }

        It 'Disconnect-PagerArchive deletes the saved credential, and revokes only the access token' {
            $null = Connect-PagerArchive -Uri $origin -ServiceAccount (New-Credential)
            Disconnect-PagerArchive
            @(Get-ChildItem -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR).Count | Should -Be 0
            Should -Invoke -ModuleName PagerArchive Invoke-PaTokenRequest -Times 1 -Exactly -ParameterFilter { $Uri -eq 'https://auth.example.test/r' -and $Form.token_type_hint -eq 'access_token' }
            Should -Invoke -ModuleName PagerArchive Invoke-PaTokenRequest -Times 0 -Exactly -ParameterFilter { $Uri -eq 'https://auth.example.test/r' -and $Form.token_type_hint -eq 'refresh_token' }
        }
    }
}
