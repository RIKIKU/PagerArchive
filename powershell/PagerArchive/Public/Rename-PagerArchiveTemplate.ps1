function Rename-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Renames a template.

    .DESCRIPTION
    Gives a template a new name. Every alias using it keeps it, and shows
    the new name at once (R28, R64). Template names are unique, so a name
    another template has is refused (template_exists). The agency's name,
    shown beside its pages, is changed with Set-PagerArchiveTemplate
    -Agency.

    A template piped from Get-PagerArchiveTemplate, or any object with an Id,
    is chosen by its Id: exactly that template, even when another template's
    name differs only in case. A name typed or piped as text is matched as
    -Name says. -WhatIf and the confirmation name the template with its Id.

    .EXAMPLE
    Rename-PagerArchiveTemplate -Name 'Example Fire' -NewName 'Example Fire North'

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'Example Fire' | Rename-PagerArchiveTemplate -NewName 'Example Fire North' -PassThru

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'example fire' | Where-Object -Property Id -EQ 49 | Rename-PagerArchiveTemplate -NewName 'Example Fire South' -WhatIf

    What if: Performing the operation "Rename template to 'Example Fire South'" on target "example fire (Id 49)".

    .INPUTS
    PagerArchive.Template, or an object with an Id property, chosen by Id.
    System.String. A template name, by value or by the Name property of an
    object without an Id.

    .OUTPUTS
    None, or PagerArchive.Template with -PassThru.

    .LINK
    Set-PagerArchiveTemplate

    .LINK
    Get-PagerArchiveTemplate
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium', DefaultParameterSetName = 'Id')]
    [OutputType('PagerArchive.Template')]
    param(
        # The template to rename, matched case-insensitively. No wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'Name')]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The Id of the template to rename, exactly. A piped object's Id
        # property binds here, and when it does, the Id chooses the template
        # and its Name is ignored. No wildcards.
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Name')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'Id')]
        [PSDefaultValue(Help = 'none')]
        [long] $Id,

        # The new name, 1 to 100 characters. No wildcards.
        [Parameter(Mandatory, Position = 1)]
        [ValidateNotNullOrEmpty()]
        [string] $NewName,

        # Output the renamed template. Off by default.
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
        if ($Id) {
            try {
                $template = Select-PaTemplate -From $templates -Id $Id
            } catch {
                $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Id))
                return
            }
        } else {
            $template = $templates | Where-Object -FilterScript { $_.Name -eq $Name } | Select-Object -First 1
            if (-not $template) {
                $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'no_template' -Category ObjectNotFound -Message "No template is called '$Name'." -TargetObject $Name))
                return
            }
        }
        if (-not $PSCmdlet.ShouldProcess((Get-PaTemplateTarget -Template $template), "Rename template to '$NewName'")) {
            return
        }
        try {
            $null = Invoke-PaApi -Method PATCH -Path "/api/v1/admin/templates/$($template.Id)" -Body @{ name = $NewName }
            # Later names in the pipeline find the template by its new name.
            $template.Name = $NewName
            if ($PassThru) {
                Get-PaTemplateList | Where-Object -FilterScript { $_.Id -eq $template.Id }
            }
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $template))
        }
    }
}
