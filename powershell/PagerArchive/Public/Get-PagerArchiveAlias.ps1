function Get-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Lists aliases.

    .DESCRIPTION
    An alias names a capcode, or a pattern of capcodes, and links it to a
    template (R27). The objects' properties match Import-PagerArchiveAlias's
    parameters, so Get-PagerArchiveAlias | Export-Csv writes a file in the
    import format, and importing it changes nothing.

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Station*'

    Pattern Name      Template     Ignored
    ------- ----      --------     -------
    0100001 Station 1 Example Fire   False
    0100002 Station 2 Example Fire   False

    .EXAMPLE
    Get-PagerArchiveAlias | Export-Csv -Path aliases.csv

    Writes every alias in the import format.

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Station*' | Get-PagerArchiveMessage -After (Get-Date).AddDays(-7)

    Messages from the last week to any of those aliases.

    .INPUTS
    System.String. Alias names by value; capcodes or patterns by the
    Pattern property.

    .OUTPUTS
    PagerArchive.Alias. Pattern (the capcode, or a pattern with % and _),
    Name, Template, Colour (the template's), Ignored and Id.

    .LINK
    New-PagerArchiveAlias

    .LINK
    Set-PagerArchiveAlias

    .LINK
    Import-PagerArchiveAlias

    .LINK
    Get-PagerArchiveMessage
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Alias')]
    param(
        # Alias names. Accepts wildcards, matched case-insensitively.
        # Default: every alias.
        [Parameter(Position = 0, ValueFromPipeline)]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

        # Capcodes or patterns, as stored (capcodes have 7 digits). Accepts
        # wildcards: * and ? as usual; the % and _ in patterns are matched
        # as written. Default: any.
        [Parameter(ValueFromPipelineByPropertyName)]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Pattern,

        # Template names. Accepts wildcards, matched case-insensitively.
        # Default: any template, or none.
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Template
    )
    begin {
        $all = $null
        $templates = @($Template | ForEach-Object -Process { [System.Management.Automation.WildcardPattern]::new($_, [System.Management.Automation.WildcardOptions]::IgnoreCase) })
    }
    process {
        try {
            $names = @($Name | ForEach-Object -Process { [System.Management.Automation.WildcardPattern]::new($_, [System.Management.Automation.WildcardOptions]::IgnoreCase) })
            $patterns = @($Pattern | ForEach-Object -Process {
                    $p = if ($_ -match '^[0-9]{1,7}$') { $_.PadLeft(7, '0') } else { $_ }
                    [System.Management.Automation.WildcardPattern]::new([System.Management.Automation.WildcardPattern]::Escape($p).Replace('`*', '*').Replace('`?', '?'), [System.Management.Automation.WildcardOptions]::IgnoreCase)
                })
            # One exact name or capcode: let the server narrow the list.
            $search = ''
            if ($names.Count -eq 1 -and -not $patterns.Count -and -not [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($Name[0])) {
                $search = $Name[0]
            } elseif ($patterns.Count -eq 1 -and -not $names.Count -and $Pattern[0] -notmatch '[*?]') {
                $search = $Pattern[0] -replace '^0+(?=\d)', ''
            }
            $source = if ($search) {
                Get-PaAliasList -Search $search
            } else {
                if ($null -eq $all) { $all = [System.Collections.Generic.List[object]]::new() }
                if ($all.Count -eq 0) {
                    Get-PaAliasList | ForEach-Object -Process { $all.Add($_); $_ }
                } else {
                    $all
                }
            }
            foreach ($alias in $source) {
                if ($names.Count -and -not ($names | Where-Object -FilterScript { $_.IsMatch($alias.Name) })) { continue }
                if ($patterns.Count -and -not ($patterns | Where-Object -FilterScript { $_.IsMatch($alias.Pattern) })) { continue }
                if ($templates.Count -and -not ($templates | Where-Object -FilterScript { $_.IsMatch($alias.Template) })) { continue }
                $alias
            }
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
        }
    }
}
