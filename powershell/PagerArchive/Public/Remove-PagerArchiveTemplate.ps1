function Remove-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Deletes templates that no alias uses.

    .DESCRIPTION
    Deletes templates. A template still used by aliases, or named by a
    waiting alias request, can't be deleted, and the error says how many use
    it (R64). Asks for confirmation unless -Confirm:$false is given.

    A template piped from Get-PagerArchiveTemplate, or any object with an Id,
    is chosen by its Id: exactly that template, even when another template's
    name differs only in case. Names typed or piped as text are matched as
    -Name says. The confirmation names each template with its Id.

    .EXAMPLE
    Remove-PagerArchiveTemplate -Name 'Example Rescue'

    .EXAMPLE
    Get-PagerArchiveTemplate | Where-Object -Property AliasCount -EQ 0 | Remove-PagerArchiveTemplate -Confirm:$false

    Deletes every template no alias uses.

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'example fire' | Where-Object -Property Id -EQ 49 | Remove-PagerArchiveTemplate -WhatIf

    What if: Performing the operation "Delete template" on target "example fire (Id 49)".

    Of two templates called 'Example Fire' and 'example fire', deletes only
    the one with the Id 49.

    .INPUTS
    PagerArchive.Template, or objects with an Id property, chosen by Id.
    System.String template names, by value or by the Name property of
    objects without an Id.

    .OUTPUTS
    None, or PagerArchive.Template (the deleted template) with -PassThru.

    .LINK
    Get-PagerArchiveTemplate

    .LINK
    Set-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High', DefaultParameterSetName = 'Id')]
    [OutputType('PagerArchive.Template')]
    param(
        # The templates to delete. Accepts wildcards, matched
        # case-insensitively.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'Name')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

        # The Ids of the templates to delete, exactly. A piped object's Id
        # property binds here, and when it does, the Id chooses the template
        # and its Name is ignored. No wildcards.
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Name')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'Id')]
        [long[]] $Id,

        # Output each deleted template. Off by default.
        [switch] $PassThru
    )
    begin {
        try {
            $templates = @(Get-PaTemplateList)
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
        }
    }
    process {
        # An Id, when one is bound (a piped record), wins over its Name.
        $selectors = if ($Id) { @($Id | ForEach-Object -Process { @{ Id = $_ } }) } else { @($Name | ForEach-Object -Process { @{ Name = $_ } }) }
        foreach ($selector in $selectors) {
            try {
                $found = @(Select-PaTemplate -From $templates @selector)
            } catch {
                $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $_.TargetObject))
                continue
            }
            foreach ($template in $found) {
                if (-not $PSCmdlet.ShouldProcess((Get-PaTemplateTarget -Template $template), 'Delete template')) {
                    continue
                }
                try {
                    $null = Invoke-PaApi -Method DELETE -Path "/api/v1/admin/templates/$($template.Id)"
                    if ($PassThru) {
                        $template
                    }
                } catch {
                    $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $template))
                }
            }
        }
    }
}
