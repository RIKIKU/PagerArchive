function New-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Adds a template.

    .DESCRIPTION
    Adds a template with an agency name and, optionally, an icon, a colour
    and an agency profile, the parser for that agency's pages (R28, R64).
    Without a profile its pages are shown as text only. The profile must be
    one the server loaded.

    The icon comes from a file (-Path or -LiteralPath) or a web address
    (-Uri). A web address is downloaded on this computer, and the file is
    uploaded: PagerArchive's server never fetches anything from another
    site (R65). PagerArchive accepts PNG, ICO, GIF, JPEG and WebP files of
    at most 100 KB and stores each as a 64x64 PNG. The icon is read or
    downloaded before the template is added, so a missing file adds
    nothing. If PagerArchive then refuses the icon, the template stays,
    without an icon, and the refusal is written as an error.

    .EXAMPLE
    New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service'

    .EXAMPLE
    New-PagerArchiveTemplate -Name 'Example Fire North' -Agency 'Example Fire Service' -Colour '#b3261e' -AgencyProfile example-fire -Path ./fire.png

    .EXAMPLE
    New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -Uri 'https://example.test/rescue.png'

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
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium', DefaultParameterSetName = 'NoIcon')]
    [OutputType('PagerArchive.Template')]
    param(
        # The template's name, 1 to 100 characters. No wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The agency's name, as shown beside its pages and as its icon's
        # text alternative. No wildcards.
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
        [string] $AgencyProfile,

        # The icon file: PNG, ICO, GIF, JPEG or WebP, at most 100 KB.
        # Accepts wildcards, which must match one file. Default: no icon.
        [Parameter(Mandatory, ParameterSetName = 'Path')]
        [Alias('PSPath')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string] $Path,

        # The icon file, taken exactly as written: no wildcards. Default: no
        # icon.
        [Parameter(Mandatory, ParameterSetName = 'LiteralPath')]
        [ValidateNotNullOrEmpty()]
        [string] $LiteralPath,

        # The icon's http or https address. It's downloaded on this computer
        # (at most 100 KB, within 30 seconds) and the file uploaded. No
        # wildcards. Default: no icon.
        [Parameter(Mandatory, ParameterSetName = 'Uri')]
        [ValidateNotNull()]
        [uri] $Uri
    )
    process {
        if (-not $PSCmdlet.ShouldProcess($Name, 'Add template')) {
            return
        }
        $icon = $null
        $source = if ($Path) { @{ Path = $Path } } elseif ($LiteralPath) { @{ LiteralPath = $LiteralPath } } elseif ($Uri) { @{ Uri = $Uri } }
        if ($source) {
            try {
                $icon = Get-PaIconData @source
            } catch {
                $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Name))
                return
            }
        }
        try {
            $body = @{ name = $Name; agency = $Agency; colour = $Colour; agency_profile = $AgencyProfile }
            $created = Invoke-PaApi -Method POST -Path '/api/v1/admin/templates' -Body $body
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Name))
            return
        }
        if ($null -ne $icon) {
            try {
                Send-PaTemplateIcon -Id $created.id -Bytes $icon
            } catch {
                $record = Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Name
                $message = "Template '$Name' was added without an icon: $($record.Exception.Message)"
                $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId $record.FullyQualifiedErrorId -Category $record.CategoryInfo.Category -Message $message -TargetObject $Name))
            }
        }
        try {
            Get-PaTemplateList | Where-Object -FilterScript { $_.Id -eq $created.id }
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Name))
        }
    }
}
