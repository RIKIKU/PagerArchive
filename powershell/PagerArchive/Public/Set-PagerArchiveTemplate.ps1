function Set-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Changes templates.

    .DESCRIPTION
    Changes a template's agency, colour or agency profile. Only the
    settings given change. Every alias using the template changes at once
    (R28). A new agency profile re-parses those aliases' pages (R23a).
    Renaming templates comes with M10's template screens.

    .EXAMPLE
    Set-PagerArchiveTemplate -Name 'Example Fire' -Colour '#b3261e'

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'Example*' | Set-PagerArchiveTemplate -AgencyProfile '' -WhatIf

    What if: Performing the operation "Change template" on target "Example Fire".

    .INPUTS
    System.String. Template names, by value or by the Name property, such
    as PagerArchive.Template objects.

    .OUTPUTS
    None, or PagerArchive.Template with -PassThru.

    .LINK
    Get-PagerArchiveTemplate

    .LINK
    New-PagerArchiveTemplate
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Template')]
    param(
        # The templates to change. Accepts wildcards, matched
        # case-insensitively.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

        # The agency's new name. Default: unchanged. No wildcards.
        [ValidateNotNullOrEmpty()]
        [string] $Agency,

        # The new colour, a name like red or a hex value like #b3261e; ''
        # removes it. Default: unchanged.
        [Alias('Color')]
        [AllowEmptyString()]
        [string] $Colour,

        # The new agency profile; '' makes its pages text only. Default:
        # unchanged.
        [AllowEmptyString()]
        [string] $AgencyProfile,

        # Output the changed template. Off by default.
        [switch] $PassThru
    )
    begin {
        try {
            $templates = @(Get-PaTemplateList)
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
        }
        $body = @{}
        if ($PSBoundParameters.ContainsKey('Agency')) { $body.agency = $Agency }
        if ($PSBoundParameters.ContainsKey('Colour')) { $body.colour = $Colour }
        if ($PSBoundParameters.ContainsKey('AgencyProfile')) { $body.agency_profile = $AgencyProfile }
    }
    process {
        foreach ($n in $Name) {
            $wild = [System.Management.Automation.WildcardPattern]::new($n, [System.Management.Automation.WildcardOptions]::IgnoreCase)
            $found = @($templates | Where-Object -FilterScript { $wild.IsMatch($_.Name) })
            if (-not $found -and -not [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($n)) {
                $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'no_template' -Category ObjectNotFound -Message "No template is called '$n'." -TargetObject $n))
            }
            foreach ($template in $found) {
                if (-not $PSCmdlet.ShouldProcess($template.Name, 'Change template')) {
                    continue
                }
                try {
                    $null = Invoke-PaApi -Method PATCH -Path "/api/v1/admin/templates/$($template.Id)" -Body $body
                    if ($PassThru) {
                        Get-PaTemplateList | Where-Object -FilterScript { $_.Id -eq $template.Id }
                    }
                } catch {
                    $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $template))
                }
            }
        }
    }
}
