function New-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Adds a template.

    .DESCRIPTION
    Adds a template with an agency name and, optionally, a colour and an
    agency profile, the parser for that agency's pages (R28, R64). Without
    a profile its pages are shown as text only. The profile must be one the
    server loaded.

    .EXAMPLE
    New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service'

    .EXAMPLE
    New-PagerArchiveTemplate -Name 'Example Fire North' -Agency 'Example Fire Service' -Colour '#b3261e' -AgencyProfile example-fire

    .INPUTS
    System.Management.Automation.PSObject. Objects with Name, Agency,
    Colour and AgencyProfile properties, such as rows from Import-Csv.

    .OUTPUTS
    PagerArchive.Template. The template added.

    .LINK
    Get-PagerArchiveTemplate

    .LINK
    Set-PagerArchiveTemplate
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Template')]
    param(
        # The template's name, 1 to 100 characters. No wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The agency's name, as shown beside its pages. No wildcards.
        [Parameter(Mandatory, ValueFromPipelineByPropertyName)]
        [ValidateNotNullOrEmpty()]
        [string] $Agency,

        # A colour name like red, or a hex value like #b3261e. Default:
        # none.
        [Parameter(ValueFromPipelineByPropertyName)]
        [Alias('Color')]
        [AllowEmptyString()]
        [string] $Colour,

        # The agency profile that parses its pages, e.g. example-fire.
        # Default: none (text only).
        [Parameter(ValueFromPipelineByPropertyName)]
        [AllowEmptyString()]
        [string] $AgencyProfile
    )
    process {
        if (-not $PSCmdlet.ShouldProcess($Name, 'Add template')) {
            return
        }
        try {
            $body = @{ name = $Name; agency = $Agency; colour = $Colour; agency_profile = $AgencyProfile }
            $created = Invoke-PaApi -Method POST -Path '/api/v1/admin/templates' -Body $body
            Get-PaTemplateList | Where-Object -FilterScript { $_.Id -eq $created.id }
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Name))
        }
    }
}
