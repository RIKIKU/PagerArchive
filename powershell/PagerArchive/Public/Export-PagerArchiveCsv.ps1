function Export-PagerArchiveCsv {
    <#
    .SYNOPSIS
    Writes objects to a CSV file, with no value a spreadsheet would run as a formula.

    .DESCRIPTION
    Works as Export-Csv, but a value that starts with =, +, -, @, a tab or a
    carriage return is written with a leading apostrophe, so opening the file
    in Excel or LibreOffice never runs it as a formula. Page text comes from
    the radio, and anyone with a transmitter can send a page that starts
    with =, so use this in place of Export-Csv for messages. It's the same
    rule as the browser's exports, and the alias import removes the
    apostrophe again, so an exported alias file imports unchanged.

    A value that isn't text is checked as Export-Csv would write it, and is
    written unchanged unless it needs the apostrophe. The objects themselves
    aren't changed.

    .EXAMPLE
    Get-PagerArchiveMessage -Text 'SHED FIRE' -First 20 | Export-PagerArchiveCsv -Path fires.csv

    .EXAMPLE
    Watch-PagerArchiveMessage -JobNumber EX0000041 | Export-PagerArchiveCsv -Path job.csv -Append

    Adds each new message about one job to a CSV file as it arrives.

    .EXAMPLE
    Get-PagerArchiveAlias | Export-PaCsv -Path aliases.csv

    Writes every alias in the import format.

    .INPUTS
    System.Management.Automation.PSObject. Any objects, such as
    PagerArchive.Message and PagerArchive.Alias.

    .OUTPUTS
    None. It writes the file.

    .LINK
    Get-PagerArchiveMessage

    .LINK
    Watch-PagerArchiveMessage

    .LINK
    Export-Csv
    #>
    [CmdletBinding(DefaultParameterSetName = 'Path')]
    [OutputType([System.Void])]
    param(
        # The CSV file to write. Required, unless -LiteralPath is given.
        [Parameter(Mandatory, Position = 0, ParameterSetName = 'Path')]
        [ValidateNotNullOrEmpty()]
        [string] $Path,

        # The CSV file to write, taken exactly as typed: no character is a
        # wildcard. Required, unless -Path is given.
        [Parameter(Mandatory, ParameterSetName = 'LiteralPath')]
        [Alias('PSPath')]
        [ValidateNotNullOrEmpty()]
        [string] $LiteralPath,

        # The objects to write, from the pipeline.
        [Parameter(Mandatory, ValueFromPipeline)]
        [AllowNull()]
        [psobject] $InputObject,

        # Adds the rows to the end of an existing file, as Export-Csv -Append
        # does. Default: the file is replaced.
        [switch] $Append
    )
    begin {
        $export = @{ Append = $Append }
        if ($PSBoundParameters.ContainsKey('Path')) {
            $export.Path = $Path
        } else {
            $export.LiteralPath = $LiteralPath
        }
        $command = $ExecutionContext.InvokeCommand.GetCommand('Microsoft.PowerShell.Utility\Export-Csv', [System.Management.Automation.CommandTypes]::Cmdlet)
        $pipeline = { & $command @export }.GetSteppablePipeline($MyInvocation.CommandOrigin)
        $pipeline.Begin($true)
    }
    process {
        if ($null -eq $InputObject) {
            return
        }
        $row = [ordered]@{}
        foreach ($property in $InputObject.PSObject.Properties) {
            $row[$property.Name] = ConvertTo-PaCsvValue -Value $property.Value
        }
        $pipeline.Process([pscustomobject]$row)
    }
    end {
        $pipeline.End()
    }
    clean {
        # After Ctrl+C (a watch), so the rows written so far are in the file.
        if ($pipeline) {
            $pipeline.Dispose()
        }
    }
}
