#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Tab completion for the small lists (R63, owner 2026-11-19), through
# TabExpansion2, the path Tab takes at the prompt, with the API replaced
# (Invoke-PaApi mocked) except where the server can't be reached.

BeforeAll {
    Import-Module -Name (Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1') -Force

    # What Tab offers at the end of a command line.
    function Complete([string] $Line) {
        @((TabExpansion2 -inputScript $Line -cursorColumn $Line.Length).CompletionMatches)
    }

    function Connect-Test {
        & (Get-Module PagerArchive) {
            $script:Connection = @{ Uri = 'https://pager.example.test'; ExpiresAt = [datetimeoffset]::UtcNow.AddHours(1); Store = 'session' }
            $script:CompletionCache = @{}
        }
    }
}

AfterAll {
    & (Get-Module PagerArchive) {
        $script:Connection = $null
        $script:CompletionCache = @{}
    }
}

Describe 'Tab completion (R63, owner 2026-11-19)' {
    BeforeEach {
        Connect-Test
        $script:templates = [System.Collections.Generic.List[object]]::new()
        $script:templates.Add([pscustomobject]@{ id = 1; name = 'Example Fire'; agency = 'Example Fire Service' })
        $script:templates.Add([pscustomobject]@{ id = 2; name = 'Spare'; agency = 'Spare' })
        $script:templates.Add([pscustomobject]@{ id = 3; name = "Example O'Neill"; agency = 'Example Fire Service' })
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -MockWith { $script:templates.ToArray() }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/agency-profiles' } -MockWith { @('example-fire', 'example-rescue') }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/meta' } -MockWith { [pscustomobject]@{ categories = @('Structure fire', 'Rescue', 'Other') } }
    }

    It 'completes template names on every command that takes one, and its Pa alias' {
        foreach ($line in 'Get-PagerArchiveAlias -Template Sp', 'New-PagerArchiveAlias -Template Sp', 'Set-PagerArchiveAlias -Template Sp',
            'Import-PagerArchiveAlias -Template Sp', 'Get-PagerArchiveTemplate -Name Sp', 'Set-PagerArchiveTemplate -Name Sp',
            'Remove-PagerArchiveTemplate -Name Sp', 'Rename-PagerArchiveTemplate -Name Sp', 'Get-PaTemplate Sp', 'Set-PaAlias -Template sp') {
            (Complete $line).CompletionText | Should -Be @('Spare') -Because $line
        }
    }
    It 'completes agencies, agency profiles and categories' {
        (Complete 'Get-PagerArchiveMessage -Agency ').ListItemText | Should -Be @('Example Fire Service', 'Spare')
        (Complete 'Watch-PagerArchiveMessage -Agency ex').ListItemText | Should -Be @('Example Fire Service')
        (Complete 'New-PagerArchiveTemplate -Agency sp').CompletionText | Should -Be @('Spare')
        (Complete 'Set-PagerArchiveTemplate -Name x -AgencyProfile ').CompletionText | Should -Be @('example-fire', 'example-rescue')
        (Complete 'New-PagerArchiveTemplate -AgencyProfile example-r').CompletionText | Should -Be @('example-rescue')
        (Complete 'Get-PagerArchiveMessage -Category ').ListItemText | Should -Be @('Other', 'Rescue', 'Structure fire')
        (Complete 'Watch-PaMessage -Category st').CompletionText | Should -Be @("'Structure fire'")
    }
    It 'completes only the small lists: not alias names, call signs, capcodes or job numbers' {
        $expected = @(
            'Get-PagerArchiveAlias:Template', 'Get-PagerArchiveMessage:Agency', 'Get-PagerArchiveMessage:Category',
            'Get-PagerArchiveTemplate:Name', 'Import-PagerArchiveAlias:Template', 'New-PagerArchiveAlias:Template',
            'New-PagerArchiveTemplate:Agency', 'New-PagerArchiveTemplate:AgencyProfile', 'Remove-PagerArchiveTemplate:Name',
            'Rename-PagerArchiveTemplate:Name', 'Set-PagerArchiveAlias:Template', 'Set-PagerArchiveTemplate:Agency',
            'Set-PagerArchiveTemplate:AgencyProfile', 'Set-PagerArchiveTemplate:Name', 'Watch-PagerArchiveMessage:Agency',
            'Watch-PagerArchiveMessage:Category'
        )
        $found = foreach ($command in Get-Command -Module PagerArchive -CommandType Function) {
            foreach ($parameter in $command.Parameters.Values) {
                if ($parameter.Attributes | Where-Object -FilterScript { $_ -is [System.Management.Automation.ArgumentCompleterAttribute] }) {
                    "$($command.Name):$($parameter.Name)"
                }
            }
        }
        @($found | Sort-Object) | Should -Be @($expected | Sort-Object)
    }

    It 'asks the server once, then answers from the cache' {
        $null = Complete 'Get-PagerArchiveAlias -Template '
        $null = Complete 'Set-PagerArchiveAlias -Template Ex'
        (Complete 'Get-PagerArchiveTemplate -Name Sp').CompletionText | Should -Be @('Spare')
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 1 -Exactly
    }
    It 'fetches again after 5 minutes' {
        $null = Complete 'Get-PagerArchiveAlias -Template '
        $script:templates.Add([pscustomobject]@{ id = 4; name = 'Example Rescue'; agency = 'Example Rescue Service' })
        # Still within 5 minutes: the cached list, without the new one.
        & (Get-Module PagerArchive) { $script:CompletionCache['Template'].FetchedAt = [datetimeoffset]::UtcNow.AddMinutes(-4) }
        (Complete 'Get-PagerArchiveAlias -Template Ex').ListItemText | Should -Be @('Example Fire', "Example O'Neill")
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 1 -Exactly
        # Over 5 minutes: fetched again.
        & (Get-Module PagerArchive) { $script:CompletionCache['Template'].FetchedAt = [datetimeoffset]::UtcNow.AddMinutes(-6) }
        (Complete 'Get-PagerArchiveAlias -Template Ex').ListItemText | Should -Be @('Example Fire', "Example O'Neill", 'Example Rescue')
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 2 -Exactly
    }
    It 'asks the server once more when nothing in the cache matches, so a new template shows up' {
        $null = Complete 'Get-PagerArchiveAlias -Template '
        $script:templates.Add([pscustomobject]@{ id = 4; name = 'New Rescue'; agency = 'New Rescue Service' })
        (Complete 'Get-PagerArchiveAlias -Template Ne').CompletionText | Should -Be @("'New Rescue'")
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 2 -Exactly
        # The refetched list is cached: the next Tab asks nothing.
        $null = Complete 'Get-PagerArchiveAlias -Template Sp'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 2 -Exactly
    }
    It 'offers nothing, not file names, when still nothing matches, after asking just once more' {
        $null = Complete 'Get-PagerArchiveAlias -Template '
        @(Complete 'Get-PagerArchiveAlias -Template Zz') | Should -HaveCount 0
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 2 -Exactly
    }
    It 'starts again for another server' {
        $null = Complete 'Get-PagerArchiveAlias -Template '
        & (Get-Module PagerArchive) { $script:Connection.Uri = 'https://other.example.test' }
        $null = Complete 'Get-PagerArchiveAlias -Template '
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -Times 2 -Exactly
    }

    It 'offers nothing, and shows no error, when not connected' {
        & (Get-Module PagerArchive) { $script:Connection = $null }
        $all = @(Complete 'Get-PagerArchiveAlias -Template ' 2>&1 3>&1 6>&1)
        $all | Should -HaveCount 0
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -Times 0 -Exactly
    }
    It 'offers nothing, and shows no error, when the API refuses' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -MockWith {
            throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'http_503' -Status 503 -Message 'PagerArchive answered 503.' })
        }
        @(Complete 'Get-PagerArchiveAlias -Template ' 2>&1 3>&1 6>&1) | Should -HaveCount 0
    }

    It 'quotes values that need it, in the quote the user started with' {
        (Complete 'Get-PagerArchiveAlias -Template Sp').CompletionText | Should -Be @('Spare')
        (Complete 'Get-PagerArchiveAlias -Template Example').CompletionText | Should -Be @("'Example Fire'", "'Example O''Neill'")
        (Complete "Get-PagerArchiveAlias -Template 'Sp").CompletionText | Should -Be @("'Spare'")
        (Complete 'Get-PagerArchiveAlias -Template "Example F').CompletionText | Should -Be @('"Example Fire"')
        $results = Complete 'Get-PagerArchiveAlias -Template Example'
        $results.ListItemText | Should -Be @('Example Fire', "Example O'Neill")
        $results.ResultType | Should -Be @('ParameterValue', 'ParameterValue')
    }
    It 'quotes as PowerShell reads it back' {
        foreach ($value in @("O'Neill", 'Fire $x', 'Fire `n', 'a "b"', '-dash', '1st', "it`u{2019}s", 'semi;colon', 'plain')) {
            $text = & (Get-Module PagerArchive) { param($v) ConvertTo-PaCompletionText -Value $v } $value
            (Invoke-Expression -Command "Write-Output -InputObject $text") | Should -BeExactly $value -Because $text
            $text = & (Get-Module PagerArchive) { param($v) ConvertTo-PaCompletionText -Value $v -Quote '"' } $value
            (Invoke-Expression -Command "Write-Output -InputObject $text") | Should -BeExactly $value -Because $text
        }
    }

    It 'offers real values only; typed wildcards narrow them where the parameter takes wildcards' {
        (Complete 'Get-PagerArchiveTemplate -Name *Fi').ListItemText | Should -Be @('Example Fire')
        (Complete 'Get-PagerArchiveMessage -Agency *serv').ListItemText | Should -Be @('Example Fire Service')
        # -Name of Rename- takes no wildcards: * is matched as itself.
        @(Complete 'Rename-PagerArchiveTemplate -Name *Fi') | Should -HaveCount 0
    }
    It 'leaves a typed pattern to work as before' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' } -MockWith {
            @([pscustomobject]@{ id = 1; name = 'Example Fire'; agency = 'Example Fire Service'; colour = $null; agency_profile = $null; icon = $null; aliases = 0 })
        }
        (Get-PagerArchiveTemplate -Name 'Ex*').Name | Should -Be 'Example Fire'
    }
}

Describe 'Tab completion when the server can''t be reached' {
    It 'offers nothing, shows no error, and doesn''t hang' {
        & (Get-Module PagerArchive) {
            $script:Connection = @{ Uri = 'http://127.0.0.1:9'; AccessToken = [securestring]::new(); ExpiresAt = [datetimeoffset]::UtcNow.AddHours(1); Store = 'session'; AllowUnencrypted = $true }
            $script:CompletionCache = @{}
        }
        $watch = [System.Diagnostics.Stopwatch]::StartNew()
        @(Complete 'Get-PagerArchiveAlias -Template ' 2>&1 3>&1 6>&1) | Should -HaveCount 0
        $watch.Elapsed.TotalSeconds | Should -BeLessThan 30
    }
}
