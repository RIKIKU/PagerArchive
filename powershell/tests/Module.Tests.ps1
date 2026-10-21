#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Behaviour of the commands with the API replaced (Invoke-PaApi mocked):
# pipeline binding, paging, the one-batch import, errors. The same
# behaviours run against the real stack in internal/stacktest.

BeforeAll {
    Import-Module -Name (Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1') -Force

    # Splits a command's output from the errors it wrote to the error
    # stream. (-ErrorVariable also collects errors caught inside the
    # command, so it isn't what a user sees.)
    function Split-Output([object[]] $All) {
        @{
            Out    = @($All | Where-Object -FilterScript { $_ -isnot [System.Management.Automation.ErrorRecord] })
            Errors = @($All | Where-Object -FilterScript { $_ -is [System.Management.Automation.ErrorRecord] })
        }
    }

    # A message as the API sends it.
    function New-ApiMessage([long] $Id, [string] $Capcode = '0100001', [string] $Alias = 'Station 1') {
        [pscustomobject]@{
            id = $Id; capcode = $Capcode; text = 'QDtest page'; display_text = 'test page'; priority = 'Emergency'; heartbeat = $false
            sent_at = '2026-10-20T01:02:03Z'; alias_id = 1; alias = $Alias; agency = 'Example Fire Service'; icon = $null; colour = $null
            ignored = $false; category = $null; format = $null; job_number = $null; event_type = $null; description = $null; address = $null
            locality = $null; grid_ref = $null; group = $null; units = $null; extra = [pscustomobject]@{}; labels = [pscustomobject]@{}
            location = $null; receptions = @([pscustomobject]@{ client = 'Receiver 1'; received_at = '2026-10-20T01:02:03Z' })
        }
    }
}

Describe 'Get-PagerArchiveMessage' {
    BeforeEach {
        $script:calls = [System.Collections.Generic.List[object]]::new()
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/messages' } -MockWith {
            $script:calls.Add($Query)
            $start = if ($Query.before) { [int]$Query.before } else { 0 }
            $messages = @(for ($i = 1; $i -le $Query.limit; $i++) { New-ApiMessage -Id ($start + $i) })
            [pscustomobject]@{ messages = $messages; next = [string]($start + $Query.limit) }
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases' } -MockWith {
            [pscustomobject]@{ more = $false; aliases = @(
                    [pscustomobject]@{ id = 1; pattern = '0100001'; name = 'Station 1'; template = 'Example Fire'; colour = $null; ignored = $false }
                    [pscustomobject]@{ id = 2; pattern = '0100002'; name = 'Station 2'; template = 'Example Fire'; colour = $null; ignored = $false }
                    [pscustomobject]@{ id = 3; pattern = '0300001'; name = 'Brigade'; template = 'WX'; colour = $null; ignored = $false }
                )
            }
        }
    }

    It 'Get-PagerArchiveAlias filters by name, pattern and template, with wildcards' {
        (Get-PagerArchiveAlias -Name 'Station*').Name | Should -Be @('Station 1', 'Station 2')
        (Get-PagerArchiveAlias -Pattern '03*').Name | Should -Be @('Brigade')
        (Get-PagerArchiveAlias -Pattern '100002').Name | Should -Be @('Station 2')
        (Get-PagerArchiveAlias -Template 'wx').Name | Should -Be @('Brigade')
        @(Get-PagerArchiveAlias).Count | Should -Be 3
    }
    It 'combines piped aliases into one request, matching each name exactly (A55)' {
        $out = @(Get-PagerArchiveAlias -Name 'Station*' | Get-PagerArchiveMessage -First 2)
        $out.Count | Should -Be 2
        $script:calls.Count | Should -Be 1
        $script:calls[0].alias_like | Should -Be @('Station 1', 'Station 2')
        $script:calls[0].limit | Should -Be 2
        $out[0].PSObject.TypeNames[0] | Should -Be 'PagerArchive.Message'
        $out[0].Text | Should -Be 'test page'
        $out[0].SentAt | Should -BeOfType [datetimeoffset]
        $out[0].Receptions[0].Client | Should -Be 'Receiver 1'
    }
    It 'takes capcodes from the pipeline' {
        $null = [pscustomobject]@{ Capcode = '0100001' }, [pscustomobject]@{ Capcode = '0100002' } | Get-PagerArchiveMessage -First 1
        $script:calls[0].capcode | Should -Be @('0100001', '0100002')
    }
    It 'turns wildcards into LIKE patterns, escaping % and _' {
        $null = Get-PagerArchiveMessage -Alias 'Station*', '100%_ sure?' -Agency 'Example*' -First 1
        $script:calls[0].alias_like | Should -Be @('Station%', '100\%\_ sure_')
        $script:calls[0].agency_like | Should -Be @('Example%')
    }
    It 'fetches pages only as the pipeline asks for more' {
        $out = @(Get-PagerArchiveMessage | Select-Object -First 250)
        $out.Count | Should -Be 250
        $script:calls.Count | Should -Be 2
        $script:calls[1].before | Should -Be '200'
    }
    It 'honours -Skip' {
        $out = @(Get-PagerArchiveMessage -Skip 3 -First 2)
        $out.Id | Should -Be @(4, 5)
    }
    It 'maps priorities, flags and switches to the API''s names' {
        $null = Get-PagerArchiveMessage -Priority None, Emergency -Flag ParseFailed -Unaliased -IncludeIgnored -After ([datetimeoffset]'2026-10-01T00:00:00+11:00') -First 1
        $q = $script:calls[0]
        $q.priority | Should -Be @('none', 'Emergency')
        $q.flag | Should -Be @('parse_failed')
        $q.unaliased | Should -Be 'true'
        $q.ignored | Should -Be 'true'
        $q.from | Should -Be '2026-10-01T00:00:00.0000000+11:00'
        $q.receptions | Should -Be 'true'
    }
}

Describe 'Import-PagerArchiveAlias' {
    BeforeAll {
        $csv = @'
"id","address","alias","agency","icon","color","pluginconf","ignore"
"1","0300001","Brigade North","WX","cloud","#1565c0","{""apikey"":""S3CRET""}","0"
"2","0300002","Tanker","WX","","","","true"
"3","","No address","WX","","","","false"
'@
    }
    BeforeEach {
        $script:sent = [System.Collections.Generic.List[object]]::new()
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases/import' } -MockWith {
            $script:sent.Add(($Body | ConvertTo-Json -Depth 5 | ConvertFrom-Json))
            $applied = -not $Body.dry_run
            [pscustomobject]@{
                rows              = @(
                    [pscustomobject]@{ index = 1; pattern = '0300001'; name = 'Brigade North'; result = 'different'; changes = @('name: "Brigade" → "Brigade North"') }
                    [pscustomobject]@{ index = 2; pattern = '0300002'; name = 'Tanker'; result = 'added' }
                    [pscustomobject]@{ index = 3; pattern = ''; name = 'No address'; result = 'invalid'; problem = 'The address isn''t a capcode or pattern (at most 7 digits, % and _).' }
                )
                conflicts         = @()
                templates_created = @()
                repeated_names    = @()
                plan              = 'p1'
                applied           = $applied
            }
        }
    }

    It 'sends the import columns only, as one batch, and outputs a result per row' {
        $split = Split-Output -All ($csv | ConvertFrom-Csv | Import-PagerArchiveAlias -Confirm:$false 2>&1)
        $out, $errors = $split.Out, $split.Errors
        $script:sent.Count | Should -Be 2
        $script:sent[0].dry_run | Should -BeTrue
        $script:sent[1].dry_run | Should -BeFalse
        $script:sent[1].plan | Should -Be 'p1'
        $rows = $script:sent[0].rows
        $rows.Count | Should -Be 3
        $rows[0].PSObject.Properties.Name | Sort-Object | Should -Be @('colour', 'ignored', 'name', 'pattern', 'template')
        ($script:sent | ConvertTo-Json -Depth 5) | Should -Not -Match 'S3CRET|apikey|cloud'
        $rows[1].ignored | Should -BeTrue
        $rows[0].ignored | Should -BeFalse
        $out.Result | Should -Be @('Different', 'Added')
        $out.Applied | Should -Be @($true, $true)
        $out[0].Changes | Should -Be @('name: "Brigade" → "Brigade North"')
        $errors.Count | Should -Be 1
        $errors[0].FullyQualifiedErrorId | Should -Be 'invalid_row,Import-PagerArchiveAlias'
        $errors[0].TargetObject.alias | Should -Be 'No address'
    }
    It 'under -WhatIf asks nothing, applies nothing, and outputs every result marked not applied (A54)' {
        $out = @($csv | ConvertFrom-Csv | Import-PagerArchiveAlias -WhatIf -ErrorAction SilentlyContinue)
        $script:sent.Count | Should -Be 1
        $script:sent[0].dry_run | Should -BeTrue
        $out.Result | Should -Be @('Different', 'Added')
        $out.Applied | Should -Be @($false, $false)
    }
    It 'reports every conflicting row as its own error, and applies nothing (A20)' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases/import' } -MockWith {
            $script:sent.Add($Body)
            [pscustomobject]@{ error = 'Some capcodes are given by rows that differ'; code = 'import_conflict'; report = [pscustomobject]@{ conflicts = @([pscustomobject]@{ pattern = '0300001'; rows = @(1, 3) }) } }
        }
        $rows = @(
            [pscustomobject]@{ address = '0300001'; alias = 'Brigade (old name)' }
            [pscustomobject]@{ address = '0300002'; alias = 'Tanker' }
            [pscustomobject]@{ address = '300001'; alias = 'Brigade' }
        )
        $split = Split-Output -All ($rows | Import-PagerArchiveAlias -Confirm:$false 2>&1)
        $out, $errors = $split.Out, $split.Errors
        $out.Count | Should -Be 0
        $script:sent.Count | Should -Be 1
        $errors.Count | Should -Be 2
        $errors | ForEach-Object -Process { $_.FullyQualifiedErrorId | Should -Be 'import_conflict,Import-PagerArchiveAlias' }
        $errors[0].CategoryInfo.Category | Should -Be 'InvalidOperation'
        $errors[0].TargetObject.alias | Should -Be 'Brigade (old name)'
        $errors[1].TargetObject.alias | Should -Be 'Brigade'
        $errors[0].Exception.Message | Should -Match 'Row 1 .*row\(s\) 3'
    }
    It 'binds PagerArchive.Alias objects (export and import round trip)' {
        $alias = [pscustomobject]@{ PSTypeName = 'PagerArchive.Alias'; Pattern = '0300002'; Name = 'Tanker'; Template = 'WX'; Colour = ''; Ignored = $true; Id = 2 }
        $null = $alias | Export-Csv -Path TestDrive:/aliases.csv
        $null = Import-Csv -Path TestDrive:/aliases.csv | Import-PagerArchiveAlias -WhatIf
        $script:sent[0].rows[0].pattern | Should -Be '0300002'
        $script:sent[0].rows[0].template | Should -Be 'WX'
        $script:sent[0].rows[0].ignored | Should -BeTrue
    }
    It 'refuses an ignore value that isn''t true or false' {
        { [pscustomobject]@{ address = '0300001'; alias = 'x'; ignore = 'maybe' } | Import-PagerArchiveAlias -WhatIf -ErrorAction Stop } | Should -Throw
    }
}

Describe 'Import-PagerArchiveAlias -Update asks as ConfirmImpact High does (guidelines section 4)' {
    BeforeAll {
        $manifest = Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1'
        # A child process with no console: a prompt fails there, so failing
        # means it asked. Invoke-PaApi is replaced inside the module.
        function Invoke-Import([string] $Arguments) {
            $script = @"
Import-Module '$manifest'
& (Get-Module PagerArchive) { function script:Invoke-PaApi { [pscustomobject]@{ rows = @([pscustomobject]@{ index = 1; pattern = '0300001'; name = 'x'; result = 'updated' }); conflicts = @(); templates_created = @(); repeated_names = @(); plan = 'p' } } }
try { `$null = [pscustomobject]@{ address = '0300001'; alias = 'x' } | Import-PagerArchiveAlias $Arguments -ErrorAction Stop; 'no prompt' } catch { 'prompted' }
"@
            & (Get-Process -Id $PID).Path -NoProfile -NonInteractive -Command $script
        }
    }
    It 'doesn''t ask without -Update' {
        Invoke-Import -Arguments '' | Should -Be 'no prompt'
    }
    It 'asks with -Update' {
        Invoke-Import -Arguments '-Update' | Should -Be 'prompted'
    }
    It 'doesn''t ask with -Update -Confirm:$false' {
        Invoke-Import -Arguments '-Update -Confirm:$false' | Should -Be 'no prompt'
    }
}

Describe 'Errors (guidelines section 7)' {
    It 'takes the category from the API''s status: <Status> <Code>' -ForEach @(
        @{ Status = 400; Code = 'x'; Category = 'InvalidArgument' }
        @{ Status = 422; Code = 'bad_name'; Category = 'InvalidArgument' }
        @{ Status = 401; Code = 'invalid_token'; Category = 'AuthenticationError' }
        @{ Status = 403; Code = 'powershell_admins_only'; Category = 'PermissionDenied' }
        @{ Status = 404; Code = 'no_alias'; Category = 'ObjectNotFound' }
        @{ Status = 409; Code = 'duplicate_alias'; Category = 'ResourceExists' }
        @{ Status = 409; Code = 'template_in_use'; Category = 'InvalidOperation' }
        @{ Status = 429; Code = 'x'; Category = 'LimitsExceeded' }
        @{ Status = 502; Code = 'x'; Category = 'ConnectionError' }
    ) {
        $record = & (Get-Module PagerArchive) { param($s, $c) New-PaErrorRecord -ErrorId $c -Status $s -Message 'm' } $Status $Code
        $record.CategoryInfo.Category | Should -Be $Category
        $record.FullyQualifiedErrorId | Should -Be $Code
    }
    It 'reports a refused item with the API''s code and carries on with the next' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' -and $Method -eq 'GET' } -MockWith {
            @([pscustomobject]@{ id = 1; name = 'Example Fire'; agency = 'Example Fire Service'; colour = $null; agency_profile = $null; aliases = 3 }
                [pscustomobject]@{ id = 2; name = 'Spare'; agency = 'Spare'; colour = $null; agency_profile = $null; aliases = 0 })
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/templates/1' } -MockWith {
            throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'template_in_use' -Status 409 -Message '"Example Fire" can''t be deleted: 3 aliases use it.' })
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/templates/2' } -MockWith { $null }
        $split = Split-Output -All (Remove-PagerArchiveTemplate -Name 'Example Fire', 'Spare' -Confirm:$false -PassThru 2>&1)
        $out, $errors = $split.Out, $split.Errors
        $out.Name | Should -Be @('Spare')
        $errors.Count | Should -Be 1
        $errors[0].FullyQualifiedErrorId | Should -Be 'template_in_use,Remove-PagerArchiveTemplate'
        $errors[0].CategoryInfo.Category | Should -Be 'InvalidOperation'
        $errors[0].Exception.Message | Should -Be '"Example Fire" can''t be deleted: 3 aliases use it.'
        $errors[0].TargetObject.Name | Should -Be 'Example Fire'
    }
}

Describe 'Signing in over plain HTTP (guidelines section 9)' {
    It 'is refused without -AllowUnencryptedAuthentication, before anything is sent' {
        Mock -ModuleName PagerArchive Invoke-RestMethod { throw 'no request should be made' }
        { Connect-PagerArchive -Uri 'http://192.0.2.1:8095' -ErrorAction Stop } | Should -Throw -ErrorId 'unencrypted_refused,Connect-PagerArchive'
        Should -Invoke -ModuleName PagerArchive Invoke-RestMethod -Times 0 -Exactly
    }
}

Describe 'Wildcards to LIKE patterns' {
    It '<Wildcard> becomes <Like>' -ForEach @(
        @{ Wildcard = 'Station*'; Like = 'Station%'; Exact = $true }
        @{ Wildcard = 'Station ?'; Like = 'Station _'; Exact = $true }
        @{ Wildcard = '100% sure_x'; Like = '100\% sure\_x'; Exact = $true }
        @{ Wildcard = 'back\slash'; Like = 'back\\slash'; Exact = $true }
        @{ Wildcard = 'literal`*star'; Like = 'literal*star'; Exact = $true }
        @{ Wildcard = 'Station [12]'; Like = 'Station _'; Exact = $false }
    ) {
        $result = & (Get-Module PagerArchive) { param($w) ConvertTo-PaLikePattern -Wildcard $w } $Wildcard
        $result.Like | Should -Be $Like
        $result.Exact | Should -Be $Exact
    }
}
