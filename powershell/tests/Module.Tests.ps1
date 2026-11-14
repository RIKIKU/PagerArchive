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
    It 'makes one API request for -First 10' {
        $out = @(Get-PagerArchiveMessage -First 10)
        $out.Count | Should -Be 10
        $script:calls.Count | Should -Be 1
        $script:calls[0].limit | Should -Be 10
    }
    It 'fetches only the pages -First needs, past the first page' {
        $out = @(Get-PagerArchiveMessage -First 250)
        $out.Count | Should -Be 250
        $script:calls.Count | Should -Be 2
        $script:calls[1].limit | Should -Be 50
    }
    It 'honours -Skip' {
        $out = @(Get-PagerArchiveMessage -Skip 3 -First 2)
        $out.Id | Should -Be @(4, 5)
        $script:calls[0].limit | Should -Be 5
    }
    It 'returns nothing, and asks nothing, for -First 0' {
        @(Get-PagerArchiveMessage -First 0).Count | Should -Be 0
        $script:calls.Count | Should -Be 0
    }
    It 'has no -IncludeTotalCount (guidelines section 3)' {
        (Get-Command -Name Get-PagerArchiveMessage).Parameters.ContainsKey('IncludeTotalCount') | Should -BeFalse
        { Get-PagerArchiveMessage -IncludeTotalCount -First 1 } | Should -Throw -ErrorId 'NamedParameterNotFound,Get-PagerArchiveMessage'
        $script:calls.Count | Should -Be 0
    }
    It 'refuses a negative -First or -Skip' {
        { Get-PagerArchiveMessage -First -1 } | Should -Throw -ErrorId 'ParameterArgumentTransformationError,Get-PagerArchiveMessage'
        { Get-PagerArchiveMessage -Skip -1 } | Should -Throw -ErrorId 'ParameterArgumentTransformationError,Get-PagerArchiveMessage'
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

Describe 'Rename-PagerArchiveTemplate (R63)' {
    BeforeEach {
        $script:names = @('Example Fire', 'Spare')
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' -and $Method -eq 'GET' } -MockWith {
            @([pscustomobject]@{ id = 1; name = $script:names[0]; agency = 'Example Fire Service'; colour = $null; agency_profile = $null; icon = $null; aliases = 3 }
                [pscustomobject]@{ id = 2; name = $script:names[1]; agency = 'Spare'; colour = $null; agency_profile = $null; icon = $null; aliases = 0 })
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/1' } -MockWith {
            $script:names[0] = $Body.name
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/2' } -MockWith {
            throw (& (Get-Module PagerArchive) { param($n) New-PaErrorRecord -ErrorId 'template_exists' -Status 409 -Message "There's already a template called `"$n`"." } $Body.name)
        }
    }

    It 'sends only the new name, with PATCH, and outputs nothing by default' {
        Rename-PagerArchiveTemplate -Name 'example fire' -NewName 'Example Fire North' | Should -BeNullOrEmpty
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter {
            $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/1' -and @($Body.Keys) -join ',' -eq 'name' -and $Body.name -eq 'Example Fire North'
        } -Times 1 -Exactly
    }
    It 'takes templates from the pipeline, and outputs the renamed template with -PassThru' {
        $t = Get-PagerArchiveTemplate -Name 'Example Fire' | Rename-PaTemplate -NewName 'Example Fire North' -PassThru
        $t.PSObject.TypeNames[0] | Should -Be 'PagerArchive.Template'
        $t.Name | Should -Be 'Example Fire North'
        $t.AliasCount | Should -Be 3
    }
    It 'reports a taken name with the API''s code, and an unknown template as no_template' {
        $split = Split-Output -All (Rename-PagerArchiveTemplate -Name 'Spare' -NewName 'Example Fire' 2>&1)
        $split.Errors.Count | Should -Be 1
        $split.Errors[0].FullyQualifiedErrorId | Should -Be 'template_exists,Rename-PagerArchiveTemplate'
        $split.Errors[0].CategoryInfo.Category | Should -Be 'ResourceExists'
        $split.Errors[0].TargetObject.Name | Should -Be 'Spare'
        { Rename-PagerArchiveTemplate -Name 'Example*' -NewName 'x' -ErrorAction Stop } | Should -Throw -ErrorId 'no_template,Rename-PagerArchiveTemplate'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/1' } -Times 0 -Exactly
    }
    It 'changes nothing under -WhatIf' {
        Rename-PagerArchiveTemplate -Name 'Example Fire' -NewName 'Example Fire North' -WhatIf
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' } -Times 0 -Exactly
    }
}

Describe 'Records piped from Get-* are chosen by Id (R63, owner 2026-11-12)' {
    BeforeAll {
        # What -WhatIf wrote to the host, read back from a transcript.
        function Get-WhatIfText([scriptblock] $Script) {
            $path = Join-Path -Path $TestDrive -ChildPath "whatif-$([guid]::NewGuid()).txt"
            $null = Start-Transcript -Path $path
            try { $null = & $Script } finally { $null = Stop-Transcript }
            @(Get-Content -Path $path | Where-Object -FilterScript { $_ -like 'What if:*' })
        }
    }
    BeforeEach {
        # Two templates whose names differ only in case, and two aliases
        # with the same name (A19).
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' -and $Method -eq 'GET' } -MockWith {
            @([pscustomobject]@{ id = 48; name = 'Example Fire'; agency = 'Example Fire Service'; colour = $null; agency_profile = $null; icon = $null; aliases = 0 }
                [pscustomobject]@{ id = 49; name = 'example fire'; agency = 'Example Fire Service'; colour = $null; agency_profile = $null; icon = $null; aliases = 0 })
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -like '/api/v1/admin/templates/*' } -MockWith { $null }
        $script:aliasRows = @(
            [pscustomobject]@{ id = 3; pattern = '0300001'; name = 'Brigade'; template_id = 48; template = 'Example Fire'; colour = $null; ignored = $false }
            [pscustomobject]@{ id = 4; pattern = '0300002'; name = 'Brigade'; template_id = 48; template = 'Example Fire'; colour = $null; ignored = $false }
        )
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases' } -MockWith {
            [pscustomobject]@{ more = $false; aliases = $script:aliasRows }
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -like '/api/v1/admin/aliases/*' -and $Method -eq 'GET' } -MockWith {
            $id = [long]($Path -replace '^.*/', '')
            $row = $script:aliasRows | Where-Object -FilterScript { $_.id -eq $id }
            if (-not $row) {
                throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'http_404' -Status 404 -Message 'no such alias' })
            }
            $row
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -like '/api/v1/admin/aliases/*' -and $Method -in 'PUT', 'DELETE' } -MockWith {
            [pscustomobject]@{ integrations = 0 }
        }
    }

    It 'takes Id from the pipeline in every parameter set, and a typed name by position' {
        foreach ($command in 'Remove-PagerArchiveTemplate', 'Set-PagerArchiveTemplate', 'Rename-PagerArchiveTemplate', 'Remove-PagerArchiveAlias', 'Set-PagerArchiveAlias', 'Rename-PagerArchiveAlias') {
            # Every set takes Id by property name, so a piped record's Id is
            # bound whichever set PowerShell picks for it.
            foreach ($set in (Get-Command -Name $command).ParameterSets) {
                $id = $set.Parameters | Where-Object -Property Name -EQ 'Id'
                $id.ValueFromPipelineByPropertyName | Should -BeTrue -Because "$command, set $($set.Name)"
            }
        }
        Remove-PagerArchiveTemplate 'example fire' -Confirm:$false
        Rename-PagerArchiveTemplate 'example fire' 'Example Fire South'
        Remove-PagerArchiveAlias 0300002 -Confirm:$false
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -in '/api/v1/admin/templates/48', '/api/v1/admin/templates/49' } -Times 2 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -like '/api/v1/admin/templates/*' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/aliases/4' } -Times 1 -Exactly
    }
    It 'removes only the piped template, not the one whose name differs in case' {
        $both = @(Get-PagerArchiveTemplate -Name 'example fire')
        $both.Count | Should -Be 2
        $both | Where-Object -Property Id -EQ 49 | Remove-PagerArchiveTemplate -Confirm:$false
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/templates/49' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/templates/48' } -Times 0 -Exactly
    }
    It 'changes only the piped template, with settings or an icon change' {
        Get-PagerArchiveTemplate | Where-Object -Property Id -EQ 49 | Set-PagerArchiveTemplate -Colour red
        Get-PagerArchiveTemplate | Where-Object -Property Id -EQ 49 | Set-PagerArchiveTemplate -RemoveIcon
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/49' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/templates/49/icon' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -like '/api/v1/admin/templates/48*' } -Times 0 -Exactly
    }
    It 'renames only the piped template' {
        Get-PagerArchiveTemplate | Where-Object -Property Id -EQ 49 | Rename-PagerArchiveTemplate -NewName 'Example Fire South'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/49' -and $Body.name -eq 'Example Fire South' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -eq '/api/v1/admin/templates/48' } -Times 0 -Exactly
    }
    It 'takes -Id typed on its own' {
        Remove-PagerArchiveTemplate -Id 49 -Confirm:$false
        Set-PagerArchiveTemplate -Id 49 -Agency 'Example Fire South Service'
        Rename-PagerArchiveTemplate -Id 49 -NewName 'Example Fire South'
        Set-PagerArchiveAlias -Id 4 -Ignored $true
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -ne 'GET' -and $Path -eq '/api/v1/admin/templates/49' } -Times 3 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -ne 'GET' -and $Path -eq '/api/v1/admin/templates/48' } -Times 0 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PUT' -and $Path -eq '/api/v1/admin/aliases/4' } -Times 1 -Exactly
    }
    It 'still matches a name given as text case-insensitively, with wildcards' {
        'example fire' | Remove-PagerArchiveTemplate -Confirm:$false
        Set-PagerArchiveTemplate -Name 'EXAMPLE*' -Colour red
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -in '/api/v1/admin/templates/48', '/api/v1/admin/templates/49' } -Times 2 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' -and $Path -in '/api/v1/admin/templates/48', '/api/v1/admin/templates/49' } -Times 2 -Exactly
    }
    It 'reports an unknown Id as no_template or no_alias' {
        { Remove-PagerArchiveTemplate -Id 7 -Confirm:$false -ErrorAction Stop } | Should -Throw -ErrorId 'no_template,Remove-PagerArchiveTemplate'
        { Rename-PagerArchiveTemplate -Id 7 -NewName 'x' -ErrorAction Stop } | Should -Throw -ErrorId 'no_template,Rename-PagerArchiveTemplate'
        { Set-PagerArchiveAlias -Id 7 -Ignored $true -ErrorAction Stop } | Should -Throw -ErrorId 'no_alias,Set-PagerArchiveAlias'
    }
    It 'removes, changes and renames only the piped alias of two with the same name' {
        $alias = Get-PagerArchiveAlias -Name 'Brigade' | Where-Object -Property Pattern -EQ '0300002'
        $alias | Remove-PagerArchiveAlias -Confirm:$false
        $alias | Set-PagerArchiveAlias -Ignored $true
        $alias | Rename-PagerArchiveAlias -NewName 'Brigade South'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/aliases/4' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PUT' -and $Path -eq '/api/v1/admin/aliases/4' -and $Body.ignored } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PUT' -and $Path -eq '/api/v1/admin/aliases/4' -and $Body.name -eq 'Brigade South' } -Times 1 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -ne 'GET' -and $Path -eq '/api/v1/admin/aliases/3' } -Times 0 -Exactly
    }
    It 'still acts on every alias with a name given as text' {
        'Brigade' | Remove-PagerArchiveAlias -Confirm:$false
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -in '/api/v1/admin/aliases/3', '/api/v1/admin/aliases/4' } -Times 2 -Exactly
    }
    It 'names each record with its Id under -WhatIf, and changes nothing' {
        $template = Get-PagerArchiveTemplate | Where-Object -Property Id -EQ 49
        $alias = Get-PagerArchiveAlias -Name 'Brigade' | Where-Object -Property Id -EQ 4
        $text = Get-WhatIfText -Script {
            $template | Remove-PagerArchiveTemplate -WhatIf
            $template | Set-PagerArchiveTemplate -Colour red -WhatIf
            $template | Rename-PagerArchiveTemplate -NewName 'Example Fire South' -WhatIf
            $alias | Remove-PagerArchiveAlias -WhatIf
            $alias | Set-PagerArchiveAlias -Ignored $true -WhatIf
            $alias | Rename-PagerArchiveAlias -NewName 'Brigade South' -WhatIf
        }
        $text | Should -Be @(
            'What if: Performing the operation "Delete template" on target "example fire (Id 49)".'
            'What if: Performing the operation "Change template" on target "example fire (Id 49)".'
            'What if: Performing the operation "Rename template to ''Example Fire South''" on target "example fire (Id 49)".'
            'What if: Performing the operation "Delete alias" on target "0300002 (Brigade, Id 4)".'
            'What if: Performing the operation "Change alias" on target "0300002 (Brigade, Id 4)".'
            'What if: Performing the operation "Rename alias to ''Brigade South''" on target "0300002 (Brigade, Id 4)".'
        )
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -ne 'GET' } -Times 0 -Exactly
    }
}

Describe 'Signing in over plain HTTP (guidelines section 9)' {
    It 'is refused without -AllowUnencryptedAuthentication, before anything is sent' {
        Mock -ModuleName PagerArchive Invoke-RestMethod { throw 'no request should be made' }
        { Connect-PagerArchive -Uri 'http://192.0.2.1:8095' -ErrorAction Stop } | Should -Throw -ErrorId 'unencrypted_refused,Connect-PagerArchive'
        Should -Invoke -ModuleName PagerArchive Invoke-RestMethod -Times 0 -Exactly
    }
}

Describe 'Reading tokens from Authentik''s replies' {
    It 'decodes JSON escapes, blanks the value, and leaves the rest readable' {
        $json = '{"device_code": "a\"b\\c\/dAe!#%", "user_code": "123456789", "interval": 5}'
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
        $secure = & (Get-Module PagerArchive) { param($b) Read-PaSecretField -Buffer $b -Name 'device_code' } $bytes
        $secure | ConvertFrom-SecureString -AsPlainText | Should -Be 'a"b\c/dAe!#%'
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        $text | Should -Not -Match 'a\\"b'
        ($text | ConvertFrom-Json).user_code | Should -Be '123456789'
        ($text | ConvertFrom-Json).device_code | Should -Match '^x+$'
    }
    It 'returns nothing for a field that isn''t there' {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes('{"error": "invalid_grant"}')
        & (Get-Module PagerArchive) { param($b) Read-PaSecretField -Buffer $b -Name 'access_token' } $bytes | Should -BeNullOrEmpty
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

Describe 'Backups: Get-PagerArchiveBackup and Start-PagerArchiveBackup (R86a)' {
    BeforeAll {
        # The API's status, as GET and POST /api/v1/admin/backup send it.
        function New-ApiBackup([string] $Run = 'idle', [string] $State = 'ok', [string] $Why = '', [string] $LastRunAt = '2026-11-14T00:04:51Z') {
            [pscustomobject]@{
                state = $State; run = $Run; last_run_at = $LastRunAt; last_good_at = '2026-11-13T17:03:12Z'; last_good_night = '2026-11-14'
                last_good_bytes = 52428800; last_good_dump_seconds = 41; last_good_check_seconds = 187; nights = 14; error = $Why
                requested_at = if ($Run -in 'queued', 'running') { '2026-11-14T00:04:10Z' } else { $null }
                running_since = if ($Run -eq 'running') { '2026-11-14T00:04:51Z' } else { $null }
            }
        }
    }
    BeforeEach {
        & (Get-Module PagerArchive) { $script:BackupPollInterval = [timespan]::Zero }
        $script:replies = [System.Collections.Generic.Queue[object]]::new()
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' -and $Path -eq '/api/v1/admin/backup' } -MockWith { New-ApiBackup -Run queued }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'GET' -and $Path -eq '/api/v1/admin/backup' } -MockWith {
            if ($script:replies.Count -gt 1) { $script:replies.Dequeue() } else { $script:replies.Peek() }
        }
    }
    AfterAll {
        & (Get-Module PagerArchive) { $script:BackupPollInterval = [timespan]::FromSeconds(5) }
    }

    It 'Get-PagerArchiveBackup outputs the state, with times as [datetimeoffset] and none as $null' {
        $script:replies.Enqueue((New-ApiBackup))
        $b = Get-PagerArchiveBackup
        $b.PSObject.TypeNames[0] | Should -Be 'PagerArchive.Backup'
        $b.State | Should -Be 'ok'
        $b.Run | Should -Be 'idle'
        $b.LastGoodAt | Should -BeOfType [datetimeoffset]
        $b.LastGoodAt.UtcDateTime | Should -Be ([datetime]::new(2026, 11, 13, 17, 3, 12, [DateTimeKind]::Utc))
        $b.RequestedAt | Should -BeNullOrEmpty
        $b.RunningSince | Should -BeNullOrEmpty
        $b.LastGoodBytes | Should -Be 52428800
        $b.LastGoodCheckSeconds | Should -Be 187
        ($b | Format-Table | Out-String) | Should -Match 'State\s+Run\s+LastGoodAt\s+LastRunAt\s+Nights\s+Error'
    }
    It '-WhatIf asks for nothing' {
        $out = @(Start-PagerArchiveBackup -WhatIf -Wait -PassThru)
        $out | Should -BeNullOrEmpty
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -Times 0 -Exactly
    }
    It 'asks once and returns at once, with nothing output unless -PassThru' {
        @(Start-PagerArchiveBackup -Confirm:$false) | Should -BeNullOrEmpty
        $b = Start-PagerArchiveBackup -PassThru -Confirm:$false
        $b.Run | Should -Be 'queued'
        $b.RequestedAt | Should -BeOfType [datetimeoffset]
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' } -Times 2 -Exactly
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'GET' } -Times 0 -Exactly
    }
    It 'refused while one is running: an error with the API''s code, and nothing output' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' } -MockWith {
            throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'backup_running' -Status 409 -Message 'A backup is running, since 11:04. Wait for its result, then ask again.' })
        }
        $r = Split-Output @(Start-PagerArchiveBackup -Wait -PassThru -Confirm:$false 2>&1)
        $r.Out | Should -BeNullOrEmpty
        $r.Errors | Should -HaveCount 1
        $r.Errors[0].FullyQualifiedErrorId | Should -Be 'backup_running,Start-PagerArchiveBackup'
        $r.Errors[0].CategoryInfo.Category | Should -Be 'InvalidOperation'
        $r.Errors[0].Exception.Message | Should -BeLike 'A backup is running, since 11:04.*'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'GET' } -Times 0 -Exactly
    }
    It 'stops when not signed in' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' } -MockWith {
            throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'not_connected' -Category AuthenticationError -Message 'Not signed in to PagerArchive.' })
        }
        { Start-PagerArchiveBackup -Confirm:$false -ErrorAction Continue } | Should -Throw -ErrorId 'not_connected,Start-PagerArchiveBackup'
    }
    It '-Wait follows it through queued and running to a good result' {
        $script:replies.Enqueue((New-ApiBackup -Run queued))
        $script:replies.Enqueue((New-ApiBackup -Run running))
        $script:replies.Enqueue((New-ApiBackup -Run idle))
        $r = Split-Output @(Start-PagerArchiveBackup -Wait -PassThru -Confirm:$false 2>&1)
        $r.Errors | Should -BeNullOrEmpty
        $r.Out | Should -HaveCount 1
        $r.Out[0].Run | Should -Be 'idle'
        $r.Out[0].State | Should -Be 'ok'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'GET' } -Times 3 -Exactly
    }
    It '-Wait writes an error when the backup failed' {
        $script:replies.Enqueue((New-ApiBackup -Run running))
        $script:replies.Enqueue((New-ApiBackup -Run idle -State failed -Why 'the check found the pagerarchive restore differs from the dump at public.messages 12'))
        $r = Split-Output @(Start-PagerArchiveBackup -Wait -Confirm:$false 2>&1)
        $r.Out | Should -BeNullOrEmpty
        $r.Errors | Should -HaveCount 1
        $r.Errors[0].FullyQualifiedErrorId | Should -Be 'backup_failed,Start-PagerArchiveBackup'
        $r.Errors[0].Exception.Message | Should -Be 'The backup failed: the check found the pagerarchive restore differs from the dump at public.messages 12.'
    }
    It '-Wait writes an error when the service finished with no result for this backup' {
        $script:replies.Enqueue((New-ApiBackup -Run idle -LastRunAt '2026-11-13T17:03:12Z'))
        $r = Split-Output @(Start-PagerArchiveBackup -Wait -Confirm:$false 2>&1)
        $r.Errors[0].FullyQualifiedErrorId | Should -Be 'backup_no_result,Start-PagerArchiveBackup'
    }
    It '-Wait gives up after -Timeout, and the backup carries on' {
        & (Get-Module PagerArchive) { $script:BackupPollInterval = [timespan]::FromMilliseconds(50) }
        $script:replies.Enqueue((New-ApiBackup -Run running))
        $r = Split-Output @(Start-PagerArchiveBackup -Wait -Timeout ([timespan]::FromMilliseconds(300)) -PassThru -Confirm:$false 2>&1)
        $r.Errors | Should -HaveCount 1
        $r.Errors[0].FullyQualifiedErrorId | Should -Be 'backup_timeout,Start-PagerArchiveBackup'
        $r.Errors[0].CategoryInfo.Category | Should -Be 'OperationTimeout'
        $r.Errors[0].Exception.Message | Should -BeLike '*It carries on: Get-PagerArchiveBackup shows how it goes.'
        $r.Out[0].Run | Should -Be 'running'
    }
    It 'refuses a -Timeout that isn''t positive' {
        { Start-PagerArchiveBackup -Wait -Timeout ([timespan]::Zero) -Confirm:$false } | Should -Throw -ErrorId 'ParameterArgumentValidationError,Start-PagerArchiveBackup'
    }
}

Describe 'Import-PagerArchiveAlias: a refusal that isn''t a conflict (R64)' {
    It 'stops with the API''s code and message, naming the template' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases/import' } -MockWith {
            [pscustomobject]@{ code = 'template_exists'; error = 'The agency "emv" differs from the template "EMV" only in letter case, and template names are unique ignoring case. Use "EMV" in the CSV, or rename the template.' }
        }
        $rows = [pscustomobject]@{ address = '0300001'; alias = 'Brigade'; agency = 'emv' }
        { $rows | Import-PagerArchiveAlias -Confirm:$false } | Should -Throw -ErrorId 'template_exists,Import-PagerArchiveAlias'
        try {
            $rows | Import-PagerArchiveAlias -Confirm:$false
        } catch {
            $_.Exception.Message | Should -BeLike '*"EMV" only in letter case*'
            $_.CategoryInfo.Category | Should -Be 'ResourceExists'
        }
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/aliases/import' } -Times 2 -Exactly
    }
}
