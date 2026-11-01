Set-StrictMode -Version Latest

# Turns a CSV's ignore column ("true", "false", "1", "0", "yes", "no" or
# empty) into on or off. A [switch] alone would turn any non-empty string,
# including "false", into on.
class PagerArchiveBoolTransformation : System.Management.Automation.ArgumentTransformationAttribute {
    [object] Transform([System.Management.Automation.EngineIntrinsics] $engineIntrinsics, [object] $inputData) {
        if ($inputData -is [string]) {
            switch ($inputData.Trim().ToLowerInvariant()) {
                { $_ -in @('1', 'true', 'yes', 'y') } { return $true }
                { $_ -in @('', '0', 'false', 'no', 'n', 'null') } { return $false }
                default { throw [System.ArgumentException]::new("'$inputData' isn't true or false.") }
            }
        }
        return $inputData
    }
}

# The connection for this session (Connect-PagerArchive), or $null.
$script:Connection = $null

foreach ($folder in @('Private', 'Public')) {
    foreach ($file in Get-ChildItem -Path (Join-Path -Path $PSScriptRoot -ChildPath $folder) -Filter '*.ps1') {
        . $file.FullName
    }
}

# Each command also has a short alias with Pa in place of PagerArchive.
foreach ($command in (Get-ChildItem -Path (Join-Path -Path $PSScriptRoot -ChildPath 'Public') -Filter '*.ps1').BaseName) {
    Set-Alias -Name ($command -replace 'PagerArchive', 'Pa') -Value $command
}
