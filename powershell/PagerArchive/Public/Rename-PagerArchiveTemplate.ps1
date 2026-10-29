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

    .EXAMPLE
    Rename-PagerArchiveTemplate -Name 'Example Fire' -NewName 'Example Fire North'

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'Example Fire' | Rename-PagerArchiveTemplate -NewName 'Example Fire North' -PassThru

    .INPUTS
    System.String. A template name, by value or by the Name property, such
    as a PagerArchive.Template object.

    .OUTPUTS
    None, or PagerArchive.Template with -PassThru.

    .LINK
    Set-PagerArchiveTemplate

    .LINK
    Get-PagerArchiveTemplate
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Template')]
    param(
        # The template to rename, matched case-insensitively. No wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

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
        $template = $templates | Where-Object -FilterScript { $_.Name -eq $Name } | Select-Object -First 1
        if (-not $template) {
            $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'no_template' -Category ObjectNotFound -Message "No template is called '$Name'." -TargetObject $Name))
            return
        }
        if (-not $PSCmdlet.ShouldProcess($template.Name, "Rename template to '$NewName'")) {
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
