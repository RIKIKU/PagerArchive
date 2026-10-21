#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Where the sign-in is saved (R61, A51, guidelines section 9): a DPAPI file
# on Windows; the default SecretManagement vault on Linux and macOS, or this
# session only with none; -Vault anywhere. Authentik is replaced here; the
# stack tests sign in for real.

BeforeAll {
    $manifest = Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1'
    Import-Module -Name $manifest -Force
    $origin = 'https://pager.example.test'
    $plain = 'rt-' + [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')

    function ConvertTo-Secure([string] $Text) {
        $s = [securestring]::new()
        foreach ($c in $Text.ToCharArray()) { $s.AppendChar($c) }
        $s.MakeReadOnly()
        $s
    }

    # Every way the token could appear in a file unencrypted.
    function Get-TokenForm([string] $Text) {
        $utf16 = [System.Text.Encoding]::Unicode.GetBytes($Text)
        @(
            $Text
            [System.Text.Encoding]::Unicode.GetString($utf16)
            [System.Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($Text))
            [System.Convert]::ToBase64String($utf16)
            [System.Convert]::ToHexString($utf16)
            [System.Convert]::ToHexString([System.Text.Encoding]::UTF8.GetBytes($Text))
        )
    }

    function Find-Token([string] $Folder, [string] $Text) {
        foreach ($file in Get-ChildItem -LiteralPath $Folder -Recurse -File -ErrorAction SilentlyContinue) {
            $bytes = [System.IO.File]::ReadAllBytes($file.FullName)
            $asText = [System.Text.Encoding]::UTF8.GetString($bytes) + [System.Text.Encoding]::Unicode.GetString($bytes)
            foreach ($form in Get-TokenForm -Text $Text) {
                if ($asText.IndexOf($form, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
                    $file.FullName
                }
            }
        }
    }

    # Connect-PagerArchive with Authentik and the API replaced.
    function Connect-Fake([string] $Vault) {
        Mock -ModuleName PagerArchive Get-PaServerSetting {
            @{ Issuer = 'https://auth.example.test/application/o/pagerarchive-powershell/'; ClientId = 'pagerarchive-powershell'; Scope = 'openid'
                Endpoints = @{ Authorize = 'https://auth.example.test/a'; Token = 'https://auth.example.test/t'; Revocation = 'https://auth.example.test/r'; Device = 'https://auth.example.test/d' }
            }
        }
        Mock -ModuleName PagerArchive Invoke-PaBrowserSignIn { @{ Status = 200; access_token = (ConvertTo-Secure -Text 'access'); refresh_token = (ConvertTo-Secure -Text $plain); expires_in = 300 } }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/me' } { [pscustomobject]@{ id = 1; email = 'owner@example.test'; name = 'Owner'; role = 'admin' } }
        $arguments = @{ Uri = $origin }
        if ($Vault) { $arguments.Vault = $Vault }
        Connect-PagerArchive @arguments
    }
}

Describe 'Saving the sign-in (R61)' {
    BeforeEach {
        $env:PAGERARCHIVE_SIGNIN_DIR = Join-Path -Path $TestDrive -ChildPath ('SignIns-' + [guid]::NewGuid().ToString('N'))
        & (Get-Module PagerArchive) { $script:Connection = $null }
    }

    AfterAll {
        Remove-Item -Path Env:PAGERARCHIVE_SIGNIN_DIR -ErrorAction SilentlyContinue
    }

    Describe 'On Windows: a DPAPI-encrypted file' -Skip:(-not $IsWindows) {
        It 'saves the sign-in in a file with no token in it unencrypted' {
            $connection = Connect-Fake
            $connection.SavedIn | Should -Be 'DPAPI file'
            $files = @(Get-ChildItem -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR)
            $files.Count | Should -Be 1
            (Get-Content -LiteralPath $files[0].FullName -Raw | ConvertFrom-Json).store | Should -Be 'dpapi'
            Find-Token -Folder $env:PAGERARCHIVE_SIGNIN_DIR -Text $plain | Should -BeNullOrEmpty
            Find-Token -Folder $env:PAGERARCHIVE_SIGNIN_DIR -Text 'access' | Should -BeNullOrEmpty
        }
        It 'is read back in a new PowerShell window' {
            $null = Connect-Fake
            $script = "Import-Module '$manifest'; & (Get-Module PagerArchive) { (Read-PaRefreshToken -Origin '$origin').RefreshToken | ConvertFrom-SecureString -AsPlainText }"
            $read = & (Get-Process -Id $PID).Path -NoProfile -NonInteractive -Command $script
            $read | Should -Be $plain
        }
        It 'is deleted, and revoked, by Disconnect-PagerArchive' {
            $null = Connect-Fake
            Mock -ModuleName PagerArchive Invoke-PaTokenRequest { @{ Status = 200 } }
            Disconnect-PagerArchive
            @(Get-ChildItem -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR).Count | Should -Be 0
            Should -Invoke -ModuleName PagerArchive Invoke-PaTokenRequest -ParameterFilter { $Uri -eq 'https://auth.example.test/r' -and $Form.token_type_hint -eq 'refresh_token' } -Times 1 -Exactly
        }
        It 'needs no SecretManagement' {
            Mock -ModuleName PagerArchive Import-PaSecretManagement { throw 'SecretManagement was loaded' }
            { Connect-Fake } | Should -Not -Throw
        }
    }

    Describe 'Off Windows: never ConvertFrom-SecureString with no key' -Skip:($IsWindows) {
        It 'is because it doesn''t encrypt there: its output decodes to the text' {
            $hex = ConvertFrom-SecureString -SecureString (ConvertTo-Secure -Text $plain)
            [System.Text.Encoding]::Unicode.GetString([System.Convert]::FromHexString($hex)) | Should -Be $plain
        }
        It 'is refused by the module''s own save and read' {
            { & (Get-Module PagerArchive) { param($t) Protect-PaToken -Token $t } (ConvertTo-Secure -Text $plain) } | Should -Throw -ErrorId 'dpapi_windows_only'
            { & (Get-Module PagerArchive) { Unprotect-PaToken -Encrypted '00' } } | Should -Throw -ErrorId 'dpapi_windows_only'
        }
        It 'with no vault: signs in for this session only, warns, writes nothing and never calls it (A51)' {
            Mock -ModuleName PagerArchive Import-PaSecretManagement { $false }
            Mock -ModuleName PagerArchive ConvertFrom-SecureString { throw 'ConvertFrom-SecureString was called' }
            $connection = Connect-Fake -WarningVariable warnings -WarningAction SilentlyContinue
            $connection.SavedIn | Should -Be 'This session only'
            ($warnings -join ' ') | Should -Match 'No SecretManagement vault is registered.*Register-SecretVault'
            Should -Invoke -ModuleName PagerArchive ConvertFrom-SecureString -Times 0 -Exactly
            Test-Path -LiteralPath $env:PAGERARCHIVE_SIGNIN_DIR | Should -BeFalse
            # This session works; a new one has to sign in.
            (& (Get-Module PagerArchive) { $script:Connection.Store }) | Should -Be 'session'
            $script = "Import-Module '$manifest'; try { Get-PagerArchiveTemplate -ErrorAction Stop } catch { `$_.FullyQualifiedErrorId }"
            & (Get-Process -Id $PID).Path -NoProfile -NonInteractive -Command $script | Should -Be 'not_connected,Get-PagerArchiveTemplate'
        }
        It 'with a registered vault but SecretManagement missing from a new window, says so' {
            Mock -ModuleName PagerArchive Import-PaSecretManagement { $false }
            $null = New-Item -ItemType Directory -Path $env:PAGERARCHIVE_SIGNIN_DIR
            & (Get-Module PagerArchive) { param($o) [System.IO.File]::WriteAllText((Get-PaSignInPath -Origin $o), (@{ uri = $o; store = 'vault'; vault = 'SecretStore'; version = 'v' } | ConvertTo-Json)) } $origin
            { & (Get-Module PagerArchive) { param($o) Read-PaRefreshToken -Origin $o } $origin } | Should -Throw -ErrorId 'secretmanagement_missing'
        }
    }

    Describe '-Vault' {
        It 'without SecretManagement installed stops with a clear error' {
            Mock -ModuleName PagerArchive Import-PaSecretManagement { $false }
            Mock -ModuleName PagerArchive Invoke-PaBrowserSignIn { throw 'should not sign in' }
            { Connect-Fake -Vault 'SecretStore' } | Should -Throw -ErrorId 'secretmanagement_missing,Connect-PagerArchive'
            Should -Invoke -ModuleName PagerArchive Invoke-PaBrowserSignIn -Times 0 -Exactly
        }
    }

    Describe 'Off Windows, with a SecretStore vault (A51)' -Skip:($IsWindows -or -not (Get-Module -ListAvailable -Name Microsoft.PowerShell.SecretStore)) {
        BeforeAll {
            # A vault of its own, in a home of its own, in child processes.
            $home2 = Join-Path -Path $TestDrive -ChildPath 'home'
            $null = New-Item -ItemType Directory -Path $home2 -Force
            $signIns = Join-Path -Path $TestDrive -ChildPath 'vault-signins'
            function Invoke-Child([string] $Script) {
                $env:HOME, $saved = $home2, $env:HOME
                $env:PAGERARCHIVE_SIGNIN_DIR = $signIns
                try {
                    & (Get-Process -Id $PID).Path -NoProfile -NonInteractive -Command $Script
                } finally {
                    $env:HOME = $saved
                }
            }
            $fake = @"
    & (Get-Module PagerArchive) {
        function script:Get-PaServerSetting { @{ Issuer = 'https://auth.example.test/'; ClientId = 'c'; Scope = 's'; Endpoints = @{ Token = 'https://auth.example.test/t'; Revocation = 'https://auth.example.test/r' } } }
        function script:Invoke-PaBrowserSignIn { `$a = [securestring]::new(); 'access'.ToCharArray() | ForEach-Object { `$a.AppendChar(`$_) }; `$r = [securestring]::new(); '$plain'.ToCharArray() | ForEach-Object { `$r.AppendChar(`$_) }; @{ Status = 200; access_token = `$a; refresh_token = `$r; expires_in = 300 } }
        function script:Invoke-PaApi { [pscustomobject]@{ id = 1; email = 'owner@example.test'; name = 'Owner'; role = 'admin' } }
        function script:Invoke-PaTokenRequest { @{ Status = 200 } }
    }
"@
            Invoke-Child -Script @'
    Register-SecretVault -Name TestStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault
    Set-SecretStoreConfiguration -Scope CurrentUser -Authentication None -Interaction None -Confirm:$false
'@
        }
        It 'saves the sign-in in the default vault, and a new window reads it' {
            $out = Invoke-Child -Script "Import-Module '$manifest'; $fake; (Connect-PagerArchive -Uri '$origin').SavedIn"
            $out | Should -Be 'Vault TestStore'
            $read = Invoke-Child -Script "Import-Module '$manifest'; & (Get-Module PagerArchive) { (Read-PaRefreshToken -Origin '$origin').RefreshToken | ConvertFrom-SecureString -AsPlainText }"
            $read | Should -Be $plain
            Find-Token -Folder $signIns -Text $plain | Should -BeNullOrEmpty
            Find-Token -Folder $home2 -Text $plain | Should -BeNullOrEmpty
        }
        It 'is removed from the vault by Disconnect-PagerArchive' {
            Invoke-Child -Script "Import-Module '$manifest'; $fake; Disconnect-PagerArchive -Uri '$origin'"
            Invoke-Child -Script "Get-SecretInfo -Vault TestStore | Measure-Object | ForEach-Object Count" | Should -Be 0
            @(Get-ChildItem -LiteralPath $signIns).Count | Should -Be 0
        }
    }
}
