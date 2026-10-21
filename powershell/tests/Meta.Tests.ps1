#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# The [pester] rules of docs/powershell-guidelines.md, checked for every
# exported command. Run with: Invoke-Pester -Path powershell/tests

BeforeDiscovery {
    $root = Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive'
    Import-Module -Name (Join-Path -Path $root -ChildPath 'PagerArchive.psd1') -Force
    $commands = @(Get-Command -Module PagerArchive -CommandType Function | ForEach-Object -Process { @{ Name = $_.Name } })
    $files = @(Get-ChildItem -Path $root -Recurse -Include '*.ps1', '*.psm1', '*.psd1' |
            ForEach-Object -Process { @{ Path = $_.FullName; Name = $_.Name } })
}

BeforeAll {
    $root = Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive'
    $manifestPath = Join-Path -Path $root -ChildPath 'PagerArchive.psd1'
    Import-Module -Name $manifestPath -Force
    $manifest = Import-PowerShellDataFile -Path $manifestPath
    [xml]$formats = Get-Content -Path (Join-Path -Path $root -ChildPath 'PagerArchive.Format.ps1xml') -Raw
    [xml]$types = Get-Content -Path (Join-Path -Path $root -ChildPath 'PagerArchive.Types.ps1xml') -Raw
    $common = [System.Management.Automation.PSCmdlet]::CommonParameters + [System.Management.Automation.PSCmdlet]::OptionalCommonParameters + @('First', 'Skip', 'IncludeTotalCount')

    function Get-Binding([string] $Name) {
        (Get-Command -Name $Name).ScriptBlock.Attributes | Where-Object -FilterScript { $_ -is [System.Management.Automation.CmdletBindingAttribute] }
    }
}

Describe 'The manifest (guidelines sections 1 and 10)' {
    It 'is valid' {
        { Test-ModuleManifest -Path $manifestPath -ErrorAction Stop } | Should -Not -Throw
    }
    It 'is for PowerShell 7.6 or later, Core only' {
        $manifest.CompatiblePSEditions | Should -Be @('Core')
        $manifest.PowerShellVersion | Should -Be '7.6'
    }
    It 'requires no other module (SecretManagement is loaded only when needed)' {
        $manifest.ContainsKey('RequiredModules') | Should -BeFalse
    }
    It 'lists its functions exactly, one per file in Public' {
        $public = (Get-ChildItem -Path (Join-Path -Path $root -ChildPath 'Public') -Filter '*.ps1').BaseName | Sort-Object
        $manifest.FunctionsToExport | Should -Not -Contain '*'
        @($manifest.FunctionsToExport | Sort-Object) | Should -Be @($public)
        @((Get-Command -Module PagerArchive -CommandType Function).Name | Sort-Object) | Should -Be @($public)
    }
    It 'lists its aliases exactly' {
        $manifest.AliasesToExport | Should -Not -Contain '*'
        @($manifest.AliasesToExport | Sort-Object) | Should -Be @($manifest.FunctionsToExport | ForEach-Object -Process { $_ -replace 'PagerArchive', 'Pa' } | Sort-Object)
    }
    It 'has about_PagerArchive' {
        Join-Path -Path $root -ChildPath 'en-US/about_PagerArchive.help.txt' | Should -Exist
        (Get-Help -Name about_PagerArchive | Out-String) | Should -Match 'Connect-PagerArchive'
    }
}

Describe '<Name> (guidelines sections 2 to 8)' -ForEach $commands {
    BeforeAll {
        $command = Get-Command -Name $Name
        $binding = Get-Binding -Name $Name
        $verb, $noun = $Name -split '-', 2
        $help = Get-Help -Name $Name -Full
        $source = $command.ScriptBlock.ToString()
        $changes = $verb -in @('New', 'Set', 'Remove', 'Rename', 'Import')
    }

    It 'uses an approved verb' {
        (Get-Verb).Verb | Should -Contain $verb
    }
    It 'has a noun starting with PagerArchive' {
        $noun | Should -Match '^PagerArchive'
    }
    It 'has a Pa alias' {
        $alias = Get-Alias -Name ($Name -replace 'PagerArchive', 'Pa') -ErrorAction SilentlyContinue
        $alias.ResolvedCommandName | Should -Be $Name
    }
    It 'is an advanced function' {
        $command.CmdletBinding | Should -BeTrue
    }
    It 'declares a PagerArchive output type' {
        $output = @($command.OutputType.Name)
        if ($Name -eq 'Disconnect-PagerArchive') {
            $output | Should -Be @('System.Void')
        } else {
            $output | Should -Not -BeNullOrEmpty
            $output | ForEach-Object -Process { $_ | Should -Match '^PagerArchive\.' }
        }
    }
    It 'takes pipeline input in every parameter set, with at most one ValueFromPipeline' {
        foreach ($set in $command.ParameterSets) {
            $pipeline = @($set.Parameters | Where-Object -FilterScript { $_.ValueFromPipeline -or $_.ValueFromPipelineByPropertyName })
            $byValue = @($set.Parameters | Where-Object -FilterScript { $_.ValueFromPipeline })
            $pipeline.Count | Should -BeGreaterThan 0 -Because "parameter set $($set.Name)"
            $byValue.Count | Should -BeLessOrEqual 1 -Because "parameter set $($set.Name)"
        }
    }
    It 'supports ShouldProcess when it changes data, with the right ConfirmImpact' {
        if ($changes) {
            $binding.SupportsShouldProcess | Should -BeTrue
            $expected = if ($verb -eq 'Remove') { 'High' } else { 'Medium' }
            $binding.ConfirmImpact | Should -Be $expected
        } else {
            $binding.SupportsShouldProcess | Should -BeFalse
        }
    }
    It 'has -PassThru when it changes, renames or removes' {
        if ($verb -in @('Set', 'Rename', 'Remove')) {
            $command.Parameters.Keys | Should -Contain 'PassThru'
        }
    }
    It 'accepts wildcards on -Name where it selects existing items' {
        if ($verb -in @('Get', 'Set', 'Remove') -and $command.Parameters.ContainsKey('Name')) {
            $command.Parameters['Name'].Attributes.TypeId.Name | Should -Contain 'SupportsWildcardsAttribute'
        }
    }
    It 'supports paging when it can return many items' {
        $binding.SupportsPaging | Should -Be ($Name -eq 'Get-PagerArchiveMessage')
    }
    It 'has full comment-based help' {
        $help.Synopsis | Should -Not -BeNullOrEmpty
        $help.Synopsis | Should -Not -Match "^\s*$Name\s"
        ($help.description | Out-String).Trim() | Should -Not -BeNullOrEmpty
        @($help.examples.example).Count | Should -BeGreaterThan 0
        ($help.inputTypes | Out-String).Trim() | Should -Not -BeNullOrEmpty
        ($help.returnValues | Out-String).Trim() | Should -Not -BeNullOrEmpty
        @($help.relatedLinks.navigationLink).Count | Should -BeGreaterThan 0
        foreach ($type in $command.OutputType.Name | Where-Object -FilterScript { $_ -like 'PagerArchive.*' }) {
            ($help.returnValues | Out-String) | Should -Match ([regex]::Escape($type))
        }
    }
    It 'never uses .PARAMETER, and documents every parameter' {
        $source | Should -Not -Match '\.PARAMETER'
        foreach ($parameter in $command.Parameters.Keys | Where-Object -FilterScript { $_ -notin $common }) {
            $text = ($help.parameters.parameter | Where-Object -Property name -EQ $parameter).description | Out-String
            $text.Trim() | Should -Not -BeNullOrEmpty -Because "-$parameter needs help"
        }
    }
    It 'has a table view and a default property set for each output type' {
        foreach ($type in $command.OutputType.Name | Where-Object -FilterScript { $_ -like 'PagerArchive.*' }) {
            @($formats.Configuration.ViewDefinitions.View | Where-Object -FilterScript { $_.ViewSelectedBy.TypeName -eq $type -and $_.TableControl }).Count | Should -Be 1 -Because $type
            $set = ($types.Types.Type | Where-Object -Property Name -EQ $type).Members.MemberSet.Members.PropertySet
            $set.Name | Should -Be 'DefaultDisplayPropertySet' -Because $type
        }
    }
}

Describe 'Objects and parameters line up (guidelines section 3)' {
    It 'an alias''s properties bind the import''s parameters' {
        $import = Get-Command -Name Import-PagerArchiveAlias
        foreach ($property in @('Pattern', 'Name', 'Template', 'Colour', 'Ignored')) {
            $import.Parameters[$property].ParameterSets.Values.ValueFromPipelineByPropertyName | Should -Contain $true -Because $property
        }
    }
    It 'the import CSV''s column names are aliases of the import''s parameters' {
        $import = Get-Command -Name Import-PagerArchiveAlias
        $import.Parameters['Pattern'].Aliases | Should -Contain 'Address'
        $import.Parameters['Name'].Aliases | Should -Contain 'Alias'
        $import.Parameters['Template'].Aliases | Should -Contain 'Agency'
        $import.Parameters['Colour'].Aliases | Should -Contain 'Color'
        $import.Parameters['Ignored'].Aliases | Should -Contain 'Ignore'
    }
}

Describe 'PSScriptAnalyzer: <Name> (guidelines section 11)' -ForEach $files {
    It 'has no findings' {
        $settings = Join-Path -Path $PSScriptRoot -ChildPath '../PSScriptAnalyzerSettings.psd1'
        # PSUseCorrectCasing sometimes throws a NullReferenceException inside
        # PSScriptAnalyzer 1.25.0 while looking up commands; a retry passes.
        for ($attempt = 1; $attempt -le 3; $attempt++) {
            try {
                $findings = @(Invoke-ScriptAnalyzer -Path $Path -Settings $settings -ErrorAction Stop)
                break
            } catch [System.NullReferenceException] {
                if ($attempt -eq 3) { throw }
            }
        }
        $findings | ForEach-Object -Process { Write-Warning -Message ('{0}:{1} {2} {3}' -f $_.ScriptName, $_.Line, $_.RuleName, $_.Message) }
        $findings.Count | Should -Be 0
    }
}
