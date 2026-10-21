function Import-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Imports aliases from the pipeline, as one batch.

    .DESCRIPTION
    Takes alias rows from the pipeline, usually from Import-Csv, and applies
    them together (R27a, R30, R58, R63):

    - New capcodes and patterns are added. One template is created for each
      new agency, with the profile -AgencyProfile maps it to.
    - Existing aliases are left as they are, and each difference is
      reported as a "Different" result. With -Update, differences are
      applied ("Updated"), and recorded in each alias's history as yours.
    - Rows for the same capcode that are identical are squashed into the
      first ("Squashed"). Rows for the same capcode that differ are a
      conflict: nothing is imported, and each row involved is reported as
      an error, so you can leave out or fix rows in the pipeline (for
      example with Where-Object) and import again.
    - A row without a valid capcode or pattern, or without a name, is
      reported as an error and skipped.

    It asks once for the whole batch, with a summary such as "412 aliases:
    380 new, 12 different". -WhatIf is the dry run: nothing changes, not
    even new capcodes, and every row's result is still output, with Applied
    $false, so you can filter the results before importing for real.

    The CSV's columns bind by name: address, alias, agency, color and
    ignore. Other columns (an icon name, an export's id) are never read.

    .EXAMPLE
    Import-Csv -Path aliases.csv | Import-PagerArchiveAlias -AgencyProfile @{ 'Example Fire' = 'example-fire' }

    Index Result  Pattern Name              Changes Applied
    ----- ------  ------- ----              ------- -------
        1 Added   0300001 Testville Brigade            True
        2 Added   0300002 Testville Tanker             True

    .EXAMPLE
    Import-Csv -Path aliases.csv | Import-PagerArchiveAlias -WhatIf | Where-Object -Property Result -EQ 'Different'

    Shows how existing aliases differ from the file, changing nothing.

    .EXAMPLE
    Import-Csv -Path aliases.csv | Import-PagerArchiveAlias -AgencyProfile (Get-Content -Path profiles.json -Raw | ConvertFrom-Json -AsHashtable)

    Loads the agency-to-profile mapping from a JSON file the deployment
    keeps.

    .EXAMPLE
    Import-Csv -Path aliases.csv | Import-PagerArchiveAlias -Update

    Also applies the differences to existing aliases, after asking.

    .INPUTS
    System.Management.Automation.PSObject. Rows with Pattern (or address),
    Name (or alias), Template (or agency), Colour (or color) and Ignored
    (or ignore) properties, such as Import-Csv's rows or
    PagerArchive.Alias objects.

    .OUTPUTS
    PagerArchive.ImportResult. One per row: Index (the row's position in the
    pipeline, from 1), Result (Added, Unchanged, Different, Updated or
    Squashed), Pattern, Name, Changes (each difference, e.g.
    name: "Brigade" -> "Brigade North"), SquashedInto (the index of the row
    a squashed row matched) and Applied ($false under -WhatIf or when the
    batch was declined).

    .LINK
    Get-PagerArchiveAlias

    .LINK
    New-PagerArchiveAlias

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.ImportResult')]
    param(
        # The capcode (1 to 7 digits) or pattern (with % and _). Binds the
        # CSV's address column. No wildcards.
        [Parameter(Mandatory, ValueFromPipelineByPropertyName)]
        [Alias('Address')]
        [AllowEmptyString()]
        [string] $Pattern,

        # The alias's name. Binds the CSV's alias column. No wildcards.
        [Parameter(Mandatory, ValueFromPipelineByPropertyName)]
        [Alias('Alias')]
        [AllowEmptyString()]
        [string] $Name,

        # The template's name, also the agency's for a new template. Binds
        # the CSV's agency column. Default: none (text only).
        [Parameter(ValueFromPipelineByPropertyName)]
        [Alias('Agency')]
        [AllowEmptyString()]
        [AllowNull()]
        [string] $Template,

        # The colour for a new template. Binds the CSV's color column.
        # Default: none.
        [Parameter(ValueFromPipelineByPropertyName)]
        [Alias('Color')]
        [AllowEmptyString()]
        [AllowNull()]
        [string] $Colour,

        # Hide the alias's pages from the default message list. Binds the
        # CSV's ignore column: true, false, 1, 0, yes, no or empty. Off by
        # default.
        [Parameter(ValueFromPipelineByPropertyName)]
        [Alias('Ignore')]
        [PagerArchiveBoolTransformation()]
        [switch] $Ignored,

        # Which agency profile each new agency's template gets, as
        # @{ 'Agency name' = 'profile-name' }. Agencies not listed are text
        # only. An unknown profile name refuses the whole import. Default:
        # none.
        [hashtable] $AgencyProfile = @{},

        # Apply differences to existing aliases (R58). Asks for
        # confirmation unless -Confirm:$false is given. Off by default.
        [switch] $Update
    )
    begin {
        # -Update counts as ConfirmImpact High (guidelines section 4): ask
        # by default, as for High, unless -Confirm was given.
        if ($Update -and -not $PSBoundParameters.ContainsKey('Confirm') -and $ConfirmPreference -eq 'High') {
            $ConfirmPreference = 'Medium'
        }
        $rows = [System.Collections.Generic.List[object]]::new()
        $inputs = [System.Collections.Generic.List[object]]::new()
    }
    process {
        $rows.Add(@{ pattern = $Pattern; name = $Name; template = [string]$Template; colour = [string]$Colour; ignored = [bool]$Ignored })
        $inputs.Add($_)
    }
    end {
        if ($rows.Count -eq 0) {
            return
        }
        $profiles = @{}
        foreach ($key in $AgencyProfile.Keys) {
            $profiles[[string]$key] = [string]$AgencyProfile[$key]
        }
        $request = @{ rows = $rows.ToArray(); agency_profiles = $profiles; update = [bool]$Update; dry_run = $true; plan = '' }
        try {
            Write-Progress -Activity 'Importing aliases' -Status "Checking $($rows.Count) rows"
            $report = Invoke-PaApi -Method POST -Path '/api/v1/admin/aliases/import' -Body $request -Accept 409
            if ($report.PSObject.Properties['code']) {
                # A conflict: nothing was applied, and each row involved is
                # reported (guidelines section 7).
                foreach ($conflict in $report.report.conflicts) {
                    foreach ($index in $conflict.rows) {
                        $row = $rows[$index - 1]
                        $others = ($conflict.rows | Where-Object -FilterScript { $_ -ne $index }) -join ', '
                        $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'import_conflict' -Status 409 -TargetObject $inputs[$index - 1] `
                                    -Message "Row $index ($($conflict.pattern), '$($row.name)') differs from row(s) $others for the same capcode, so nothing was imported. Leave out or fix the rows, and import again."))
                    }
                }
                return
            }
            $summary = Get-PaImportSummary -Report $report
            $action = if ($Update) { 'Import aliases and update existing ones' } else { 'Import aliases' }
            $applied = $false
            $changes = @($report.rows | Where-Object -FilterScript { $_.result -in @('added', 'updated') }).Count
            if ($PSCmdlet.ShouldProcess($summary, $action)) {
                if ($changes -gt 0 -or @($report.templates_created).Count -gt 0) {
                    Write-Progress -Activity 'Importing aliases' -Status "Applying $($rows.Count) rows"
                    $request.dry_run = $false
                    $request.plan = $report.plan
                    $report = Invoke-PaApi -Method POST -Path '/api/v1/admin/aliases/import' -Body $request -Accept 409
                    if ($report.PSObject.Properties['code']) {
                        throw (New-PaErrorRecord -ErrorId $report.code -Status 409 -Message $report.error)
                    }
                }
                $applied = $true
            }
            Write-Progress -Activity 'Importing aliases' -Completed
            foreach ($repeated in $report.repeated_names) {
                Write-Verbose -Message "$(@($repeated.patterns).Count) aliases share one name: $($repeated.patterns -join ', ')"
            }
            foreach ($result in $report.rows) {
                if ($result.result -eq 'invalid') {
                    $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'invalid_row' -Category InvalidData -TargetObject $(if ($result.index -le $inputs.Count) { $inputs[$result.index - 1] }) `
                                -Message "Row $($result.index) was skipped: $($result.problem)"))
                    continue
                }
                [pscustomobject]@{
                    PSTypeName   = 'PagerArchive.ImportResult'
                    Index        = [int]$result.index
                    Result       = (Get-Culture).TextInfo.ToTitleCase($result.result)
                    Pattern      = $result.pattern
                    Name         = $result.name
                    Changes      = @($result.PSObject.Properties['changes'] ? $result.changes : @())
                    SquashedInto = if ($result.PSObject.Properties['squashed_into']) { [int]$result.squashed_into } else { $null }
                    Applied      = $applied
                }
            }
        } catch {
            Write-Progress -Activity 'Importing aliases' -Completed
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
        }
    }
}
