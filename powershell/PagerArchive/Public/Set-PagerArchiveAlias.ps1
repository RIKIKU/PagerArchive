function Set-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Changes aliases' templates or ignored flags.

    .DESCRIPTION
    Changes the template or the ignored flag of aliases chosen by capcode
    or pattern, or by name. Only the settings given change. To rename an
    alias, use Rename-PagerArchiveAlias. Changes take effect at once, are
    recorded in each alias's history as yours (R57), and a new template
    re-parses the alias's pages (R23a).

    .EXAMPLE
    Set-PagerArchiveAlias -Pattern 0100004 -Template 'Example Rescue'

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Station*' | Set-PagerArchiveAlias -Template 'Example Fire' -WhatIf

    What if: Performing the operation "Change alias" on target "0100001 (Station 1)".

    .EXAMPLE
    Set-PagerArchiveAlias -Name 'Network test*' -Ignored $true

    .INPUTS
    PagerArchive.Alias, or objects with a Pattern property. System.String
    alias names by value.

    .OUTPUTS
    None, or PagerArchive.Alias with -PassThru.

    .LINK
    Get-PagerArchiveAlias

    .LINK
    Rename-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium', DefaultParameterSetName = 'Pattern')]
    [OutputType('PagerArchive.Alias')]
    param(
        # Capcodes or patterns of the aliases to change, exactly. No
        # wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName, ParameterSetName = 'Pattern')]
        [Alias('Capcode')]
        [ValidateNotNullOrEmpty()]
        [string[]] $Pattern,

        # Names of the aliases to change. Accepts wildcards, matched
        # case-insensitively.
        [Parameter(Mandatory, ValueFromPipeline, ParameterSetName = 'Name')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

        # The template's name; '' removes the template (text only). Default:
        # unchanged. No wildcards.
        [AllowEmptyString()]
        [string] $Template,

        # $true hides the aliases' pages from the default message list,
        # $false shows them. Default: unchanged.
        [Nullable[bool]] $Ignored,

        # Output each changed alias. Off by default.
        [switch] $PassThru
    )
    begin {
        $templateId = $null
        try {
            if ($Template) {
                $templateId = (Find-PaTemplate -Name $Template).Id
            }
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Template))
        }
    }
    process {
        try {
            $aliases = @(Find-PaAlias -Pattern $Pattern -Name $Name)
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $_.TargetObject))
            return
        }
        foreach ($alias in $aliases) {
            if (-not $PSCmdlet.ShouldProcess("$($alias.Pattern) ($($alias.Name))", 'Change alias')) {
                continue
            }
            try {
                $current = Invoke-PaApi -Method GET -Path "/api/v1/admin/aliases/$($alias.Id)"
                $body = @{
                    pattern     = $current.pattern
                    name        = $current.name
                    template_id = $current.template_id
                    ignored     = [bool]$current.ignored
                }
                if ($PSBoundParameters.ContainsKey('Template')) { $body.template_id = $templateId }
                if ($null -ne $Ignored) { $body.ignored = [bool]$Ignored }
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
