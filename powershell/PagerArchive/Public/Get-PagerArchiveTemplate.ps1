function Get-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Lists templates.

    .DESCRIPTION
    A template holds an agency's name, colour and agency profile (R28).
    Aliases use templates, and editing a template changes every alias that
    uses it. AliasCount says how many do.

    .EXAMPLE
    Get-PagerArchiveTemplate

    Name         Agency                Colour  AgencyProfile AliasCount
    ----         ------                ------  ------------- ----------
    Example Fire Example Fire Service  #b3261e example-fire           4

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'Example*'

    .INPUTS
    System.String. Template names, by value or by the Name property.

    .OUTPUTS
    PagerArchive.Template. Name, Agency, Colour, AgencyProfile (the parser
    used for its pages, or empty for text only), AliasCount and Id.

    .LINK
    New-PagerArchiveTemplate

    .LINK
    Set-PagerArchiveTemplate

    .LINK
    Remove-PagerArchiveTemplate

    .LINK
    Get-PagerArchiveAlias
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Template')]
    param(
        # Template names. Accepts wildcards, matched case-insensitively.
        # Default: every template.
        [Parameter(Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name
    )
    begin {
        try {
            $templates = @(Get-PaTemplateList)
        } catch {
            $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
        }
    }
    process {
        if (-not $Name) {
            foreach ($template in $templates) {
                $template
            }
            return
        }
        foreach ($n in $Name) {
            $wild = [System.Management.Automation.WildcardPattern]::new($n, [System.Management.Automation.WildcardOptions]::IgnoreCase)
            $found = @($templates | Where-Object -FilterScript { $wild.IsMatch($_.Name) })
            if (-not $found -and -not [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($n)) {
                $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'no_template' -Category ObjectNotFound -Message "No template is called '$n'." -TargetObject $n))
            }
            foreach ($template in $found) {
                $template
            }
        }
    }
}
