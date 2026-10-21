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

Describe 'Table and list views (guidelines section 5)' {
    It 'labels every column computed by a script, so the header isn''t the script' {
        foreach ($view in $formats.Configuration.ViewDefinitions.View) {
            $headers = @($view.TableControl.TableHeaders.TableColumnHeader)
            $items = @($view.TableControl.TableRowEntries.TableRowEntry.TableColumnItems.TableColumnItem)
            $headers.Count | Should -Be $items.Count -Because $view.Name
            for ($i = 0; $i -lt $items.Count; $i++) {
                if ($items[$i].ScriptBlock) {
                    "$($headers[$i].Label)" | Should -Not -BeNullOrEmpty -Because "$($view.Name) column $($i + 1) is computed"
                }
            }
        }
    }
    It 'shows a message''s alias, or its capcode when it has none, under "Alias"' {
        $message = [pscustomobject]@{ PSTypeName = 'PagerArchive.Message'; SentAt = [datetimeoffset]'2026-10-21T00:00:00Z'; Priority = 'Admin'; Alias = $null; Capcode = '0999999'; Text = 'x' }
        $lines = ($message | Format-Table | Out-String -Width 200) -split "`n" | Where-Object -FilterScript { $_.Trim() }
        $lines[0] | Should -Match '^SentAt\s+Priority\s+Alias\s+Text\s*$'
        $lines[2] | Should -Match '0999999'
    }
    It 'labels every list item computed by a script, so the label isn''t the script' {
        foreach ($view in $formats.Configuration.ViewDefinitions.View) {
            foreach ($item in @($view.ListControl.ListEntries.ListEntry.ListItems.ListItem | Where-Object -FilterScript { $_ })) {
                if ($item.ScriptBlock) {
                    "$($item.Label)" | Should -Not -BeNullOrEmpty -Because "a $($view.Name) list item is computed: $($item.ScriptBlock)"
                }
            }
        }
    }
    It 'shows every time as local time under its own name (owner, 2026-10-22)' {
        # Times are the properties named ...At (SentAt, ReceivedAt, ExpiresAt).
        foreach ($view in $formats.Configuration.ViewDefinitions.View) {
            $headers = @($view.TableControl.TableHeaders.TableColumnHeader)
            $shown = @(
                $columns = @($view.TableControl.TableRowEntries.TableRowEntry.TableColumnItems.TableColumnItem | Where-Object -FilterScript { $_ })
                for ($i = 0; $i -lt $columns.Count; $i++) {
                    @{ Where = "$($view.Name) column $($i + 1)"; Label = "$($headers[$i].Label)"; Property = "$($columns[$i].PropertyName)"; Script = "$($columns[$i].ScriptBlock)" }
                }
                foreach ($item in @($view.ListControl.ListEntries.ListEntry.ListItems.ListItem | Where-Object -FilterScript { $_ })) {
                    @{ Where = "$($view.Name) list item $($item.Label)$($item.PropertyName)"; Label = "$($item.Label)"; Property = "$($item.PropertyName)"; Script = "$($item.ScriptBlock)" }
                }
            )
            foreach ($s in $shown) {
                $s.Property | Should -Not -MatchExactly 'At$' -Because "$($s.Where) shows a time as it is"
                $s.Script -creplace '\.\w+At\.LocalDateTime\b', '' | Should -Not -MatchExactly '\.\w+At\b' -Because "$($s.Where) shows a time not as .LocalDateTime"
                if ($s.Script -cmatch '^\$_\.(\w+At)\b') {
                    $s.Script | Should -BeExactly "`$_.$($Matches[1]).LocalDateTime" -Because $s.Where
                    $s.Label | Should -BeExactly $Matches[1] -Because $s.Where
                }
            }
        }
    }
    It 'has a list view for messages showing what the detail view shows (R35c, R63a)' {
        $list = $formats.Configuration.ViewDefinitions.View | Where-Object -FilterScript { $_.ViewSelectedBy.TypeName -eq 'PagerArchive.Message' -and $_.ListControl }
        $list | Should -HaveCount 1
        $labels = @($list.ListControl.ListEntries.ListEntry.ListItems.ListItem | ForEach-Object -Process { "$($_.Label)$($_.PropertyName)" })
        foreach ($field in @('SentAt', 'Priority', 'Alias', 'Capcode', 'Agency', 'Text', 'Group', 'JobNumber', 'EventType', 'Category', 'Description', 'Address', 'Locality', 'GridRef', 'Units', 'Fields', 'Location', 'ParseFailed', 'AddressUnsplit', 'LocationMismatch', 'Receptions')) {
            $labels | Should -Contain $field
        }
    }
    It 'shows messages as a table by default' {
        $first = $formats.Configuration.ViewDefinitions.View | Where-Object -FilterScript { $_.ViewSelectedBy.TypeName -eq 'PagerArchive.Message' } | Select-Object -First 1
        $first.TableControl | Should -Not -BeNullOrEmpty
    }
}

Describe 'Times in a time zone that isn''t UTC (guidelines section 5)' {
    BeforeAll {
        # .NET reads the local time zone once and caches it, and on Windows it
        # ignores TZ, so the test sets the cached zone itself: Chatham Islands,
        # +13:45 in October, an offset no CI runner or developer has.
        $cache = [TimeZoneInfo].GetField('s_cachedData', [Reflection.BindingFlags]'NonPublic, Static')
        $cache | Should -Not -BeNullOrEmpty -Because 'the test needs .NET''s time zone cache'
        $null = [TimeZoneInfo]::Local
        $data = $cache.GetValue($null)
        $data.GetType().GetField('_localTimeZone', [Reflection.BindingFlags]'NonPublic, Instance').SetValue($data, [TimeZoneInfo]::FindSystemTimeZoneById('Pacific/Chatham'))
    }
    AfterAll {
        [TimeZoneInfo]::ClearCachedData()
    }
    It 'runs in Chatham time' {
        [TimeZoneInfo]::Local.BaseUtcOffset | Should -Be ([timespan]'12:45:00')
    }
    It 'shows a message''s SentAt in local time, and keeps the [datetimeoffset] the API sent' {
        $row = [pscustomobject]@{
            id = 1; capcode = '0999999'; text = 'x'; display_text = 'x'; priority = 'Admin'; heartbeat = $false
            sent_at = '2026-10-21T00:00:00Z'; alias = 'Station 4'; agency = 'Example Fire Service'; ignored = $false
            job_number = $null; event_type = $null; category = $null; description = $null; address = $null
            locality = $null; grid_ref = $null; group = $null; units = $null; extra = $null; location = $null
        }
        $message = & (Get-Module PagerArchive) { param($r) ConvertTo-PaMessage -Row $r } $row
        $lines = ($message | Format-Table | Out-String -Width 200) -split "`n" | Where-Object -FilterScript { $_.Trim() }
        $lines[0] | Should -Match '^SentAt\s+Priority\s+Alias\s+Text\s*$'
        $lines[2] | Should -BeLike "$(([datetime]'2026-10-21T13:45:00').ToString()) *"
        $lines[2] | Should -Not -Match '\+00:00'

        $message.SentAt | Should -BeOfType [datetimeoffset]
        $message.SentAt.Offset | Should -Be ([timespan]::Zero)
        $message.SentAt.UtcDateTime | Should -Be ([datetime]::new(2026, 10, 21, 0, 0, 0, [DateTimeKind]::Utc))
        ($message | Select-Object -Property SentAt | ConvertTo-Csv)[1] | Should -BeLike '*+00:00*'
    }
    It 'shows a message''s SentAt and reception times in local time in its list view' {
        $row = [pscustomobject]@{
            id = 1; capcode = '0999999'; text = 'x'; display_text = 'x'; priority = 'Admin'; heartbeat = $false
            sent_at = '2026-10-21T00:00:00Z'; alias = 'Station 4'; agency = 'Example Fire Service'; ignored = $false
            job_number = $null; event_type = $null; category = $null; description = $null; address = $null
            locality = $null; grid_ref = $null; group = $null; units = $null; extra = $null; location = $null
            receptions = @(
                [pscustomobject]@{ client = 'Receiver 1'; received_at = '2026-10-21T00:00:01Z' }
                [pscustomobject]@{ client = 'Receiver 2'; received_at = '2026-10-21T00:00:02Z' }
            )
        }
        $message = & (Get-Module PagerArchive) { param($r) ConvertTo-PaMessage -Row $r } $row
        $text = $message | Format-List | Out-String -Width 200
        $text | Should -Match "(?m)^SentAt\s+: $([regex]::Escape(([datetime]'2026-10-21T13:45:00').ToString()))\s*$"
        $text | Should -Match "(?m)^Receptions\s+: $([regex]::Escape(([datetime]'2026-10-21T13:45:01').ToString()))\s+Receiver 1\s*$"
        $text | Should -Match "(?m)^\s+$([regex]::Escape(([datetime]'2026-10-21T13:45:02').ToString()))\s+Receiver 2\s*$"
        $text | Should -Not -Match '\+00:00'

        $message.Receptions[0].ReceivedAt | Should -BeOfType [datetimeoffset]
        $message.Receptions[0].ReceivedAt.Offset | Should -Be ([timespan]::Zero)
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
