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

# Tab completion for the small lists (R63, owner 2026-11-19): template
# names, agencies, agency profiles and event categories. Every parameter
# that completes names this class; it hands over to Get-PaParameterCompletion
# (Private/Completion.ps1). A class, not a script block: a class's methods
# run inside the module, where its private functions are, and an empty
# answer offers nothing rather than file names.
class PagerArchiveCompleter : System.Management.Automation.IArgumentCompleter {
    [System.Collections.Generic.IEnumerable[System.Management.Automation.CompletionResult]] CompleteArgument(
        [string] $commandName,
        [string] $parameterName,
        [string] $wordToComplete,
        [System.Management.Automation.Language.CommandAst] $commandAst,
        [System.Collections.IDictionary] $fakeBoundParameters) {
        $results = [System.Collections.Generic.List[System.Management.Automation.CompletionResult]]::new()
        try {
            foreach ($result in @(Get-PaParameterCompletion -CommandName $commandName -ParameterName $parameterName -WordToComplete $wordToComplete)) {
                $results.Add($result)
            }
        } catch {
            # Never an error while the user is typing.
            $results.Clear()
        }
        return $results
    }
}

# The connection for this session (Connect-PagerArchive), or $null.
$script:Connection = $null

# How often Start-PagerArchiveBackup -Wait asks how the backup is going.
$script:BackupPollInterval = [timespan]::FromSeconds(5)

foreach ($folder in @('Private', 'Public')) {
    foreach ($file in Get-ChildItem -Path (Join-Path -Path $PSScriptRoot -ChildPath $folder) -Filter '*.ps1') {
        . $file.FullName
    }
}

# Each command also has a short alias with Pa in place of PagerArchive.
foreach ($command in (Get-ChildItem -Path (Join-Path -Path $PSScriptRoot -ChildPath 'Public') -Filter '*.ps1').BaseName) {
    Set-Alias -Name ($command -replace 'PagerArchive', 'Pa') -Value $command
}
