# The formula guard for Export-PagerArchiveCsv: the rule of the server's
# alias export (internal/catalog/export.go), whose import reverses it.

# What makes a spreadsheet read a cell as a formula.
$script:CsvFormulaStart = [char[]]@('=', '+', '-', '@', "`t", "`r")

# Returns a value as Export-Csv would write it, with a leading apostrophe
# when that text starts with a formula character, or else the value as it is.
function ConvertTo-PaCsvValue {
    [CmdletBinding()]
    [OutputType([string], [object], [object[]])]
    param(
        [Parameter(Mandatory)]
        [AllowNull()]
        [object] $Value
    )
    if ($null -eq $Value) {
        return $null
    }
    $text = if ($Value -is [string]) {
        $Value
    } elseif ($Value -is [System.Management.Automation.PSCustomObject] -or ($Value -is [psobject] -and $Value.PSObject.BaseObject -is [System.Management.Automation.PSCustomObject])) {
        # Export-Csv writes a custom object as @{Name=value; ...}.
        $Value.PSObject.ToString()
    } else {
        $Value.ToString()
    }
    if ($text.Length -gt 0 -and $text[0] -in $script:CsvFormulaStart) {
        return "'$text"
    }
    # The comma keeps an array whole, as Export-Csv would see it.
    return , $Value
}
