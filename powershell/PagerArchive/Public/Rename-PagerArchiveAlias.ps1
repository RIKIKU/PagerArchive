function Rename-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Renames an alias.

    .DESCRIPTION
    Gives an alias, chosen by capcode or pattern, or by name, a new name.
    Names are shown as they are now, everywhere, at once (R23a). The change
    is recorded in the alias's history as yours (R57). The same name may be
    used for several capcodes (a brigade can have several pagers).

    .EXAMPLE
    Rename-PagerArchiveAlias -Pattern 0300001 -NewName 'Brigade North'

    .EXAMPLE
    Rename-PagerArchiveAlias -Name 'Brigade' -NewName 'Brigade North' -PassThru

    .INPUTS
    PagerArchive.Alias, or objects with a Pattern property. System.String
    alias names by value.

    .OUTPUTS
    None, or PagerArchive.Alias with -PassThru.

    .LINK
    Set-PagerArchiveAlias

    .LINK
    Get-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium', DefaultParameterSetName = 'Pattern')]
    [OutputType('PagerArchive.Alias')]
    param(
        # The capcode or pattern of the alias to rename, exactly. No
        # wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName, ParameterSetName = 'Pattern')]
        [Alias('Capcode')]
        [ValidateNotNullOrEmpty()]
        [string] $Pattern,

        # The name of the alias to rename. No wildcards: a name used by
        # several aliases renames each of them.
        [Parameter(Mandatory, ValueFromPipeline, ParameterSetName = 'Name')]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The new name, 1 to 100 characters. No wildcards.
        [Parameter(Mandatory, Position = 1)]
        [ValidateNotNullOrEmpty()]
        [string] $NewName,

        # Output the renamed alias. Off by default.
        [switch] $PassThru
    )
    process {
        try {
            $escaped = if ($Name) { [System.Management.Automation.WildcardPattern]::Escape($Name) } else { $null }
            $aliases = @(Find-PaAlias -Pattern $Pattern -Name $escaped)
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $_.TargetObject))
            return
        }
        foreach ($alias in $aliases) {
            if (-not $PSCmdlet.ShouldProcess("$($alias.Pattern) ($($alias.Name))", "Rename alias to '$NewName'")) {
                continue
            }
            try {
                $current = Invoke-PaApi -Method GET -Path "/api/v1/admin/aliases/$($alias.Id)"
                $body = @{ pattern = $current.pattern; name = $NewName; template_id = $current.template_id; ignored = [bool]$current.ignored }
                $null = Invoke-PaApi -Method PUT -Path "/api/v1/admin/aliases/$($alias.Id)" -Body $body
                if ($PassThru) {
                    Get-PaAliasList -Search $alias.Pattern | Where-Object -FilterScript { $_.Id -eq $alias.Id }
                }
            } catch {
                $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $alias))
            }
        }
    }
}
