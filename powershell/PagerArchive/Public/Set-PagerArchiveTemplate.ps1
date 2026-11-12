function Set-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Changes templates.

    .DESCRIPTION
    Changes a template's agency, colour, agency profile or icon. Only the
    settings given change. Every alias using the template changes at once
    (R28). A new agency profile re-parses those aliases' pages (R23a). To
    rename a template, use Rename-PagerArchiveTemplate.

    A template piped from Get-PagerArchiveTemplate, or any object with an Id,
    is chosen by its Id: exactly that template, even when another template's
    name differs only in case. Names typed or piped as text are matched as
    -Name says. -WhatIf and the confirmation name each template with its Id.

    The icon comes from a file (-Path or -LiteralPath) or a web address
    (-Uri), and replaces the template's icon; -RemoveIcon removes it. A web
    address is downloaded on this computer, once for all the templates
    given, and the file is uploaded: PagerArchive's server never fetches
    anything from another site (R65). PagerArchive accepts PNG, ICO, GIF,
    JPEG and WebP files of at most 100 KB and stores each as a 64x64 PNG.

    .EXAMPLE
    Set-PagerArchiveTemplate -Name 'Example Fire' -Colour '#b3261e'

    .EXAMPLE
    Set-PagerArchiveTemplate -Name 'Example Fire' -Path ./fire.png

    .EXAMPLE
    Set-PagerArchiveTemplate -Name 'Example Fire' -Uri 'https://example.test/fire.png'

    Downloads the image on this computer and uploads it as the icon.

    .EXAMPLE
    Get-PagerArchiveTemplate -Name 'Example*' | Set-PagerArchiveTemplate -RemoveIcon -WhatIf

    What if: Performing the operation "Change template" on target "Example Fire (Id 1)".

    .EXAMPLE
    Set-PagerArchiveTemplate -Id 49 -Colour '#1565c0'

    Changes only the template with the Id 49, even when another template's
    name differs from its name only in case.

    .INPUTS
    PagerArchive.Template, or objects with an Id property, chosen by Id.
    System.String template names, by value or by the Name property of
    objects without an Id.

    .OUTPUTS
    None, or PagerArchive.Template with -PassThru.

    .LINK
    Get-PagerArchiveTemplate

    .LINK
    New-PagerArchiveTemplate

    .LINK
    Rename-PagerArchiveTemplate
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium', DefaultParameterSetName = 'IdSettings')]
    [OutputType('PagerArchive.Template')]
    param(
        # The templates to change. Accepts wildcards, matched
        # case-insensitively.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'Settings')]
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'Path')]
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'LiteralPath')]
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'Uri')]
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName, ParameterSetName = 'RemoveIcon')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

        # The Ids of the templates to change, exactly. A piped object's Id
        # property binds here, and when it does, the Id chooses the template
        # and its Name is ignored. No wildcards.
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Settings')]
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Path')]
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'LiteralPath')]
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Uri')]
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'RemoveIcon')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'IdSettings')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'IdPath')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'IdLiteralPath')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'IdUri')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'IdRemoveIcon')]
        [long[]] $Id,

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

        # A new icon file: PNG, ICO, GIF, JPEG or WebP, at most 100 KB.
        # Accepts wildcards, which must match one file. Default: the icon is
        # unchanged.
        [Parameter(Mandatory, ParameterSetName = 'Path')]
        [Parameter(Mandatory, ParameterSetName = 'IdPath')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string] $Path,

        # A new icon file, taken exactly as written: no wildcards. Default:
        # the icon is unchanged.
        [Parameter(Mandatory, ParameterSetName = 'LiteralPath')]
        [Parameter(Mandatory, ParameterSetName = 'IdLiteralPath')]
        [Alias('PSPath')]
        [ValidateNotNullOrEmpty()]
        [string] $LiteralPath,

        # A new icon's http or https address. It's downloaded on this
        # computer (at most 100 KB, within 30 seconds) and the file
        # uploaded. No wildcards. Default: the icon is unchanged.
        [Parameter(Mandatory, ParameterSetName = 'Uri')]
        [Parameter(Mandatory, ParameterSetName = 'IdUri')]
        [ValidateNotNull()]
        [uri] $Uri,

        # Removes the icon, so its aliases show their colour only. Off by
        # default.
        [Parameter(Mandatory, ParameterSetName = 'RemoveIcon')]
        [Parameter(Mandatory, ParameterSetName = 'IdRemoveIcon')]
        [switch] $RemoveIcon,

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
        # The icon is read or downloaded once, when the first template is
        # changed, so -WhatIf reads and downloads nothing.
        $iconSource = if ($Path) { @{ Path = $Path } } elseif ($LiteralPath) { @{ LiteralPath = $LiteralPath } } elseif ($Uri) { @{ Uri = $Uri } }
        $icon = $null
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
                if (-not $PSCmdlet.ShouldProcess((Get-PaTemplateTarget -Template $template), 'Change template')) {
                    continue
                }
                if ($iconSource -and $null -eq $icon) {
                    try {
                        $icon = Get-PaIconData @iconSource
                    } catch {
                        # The same file or address for every template: stop.
                        $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $template))
                    }
                }
                try {
                    if ($body.Count) {
                        $null = Invoke-PaApi -Method PATCH -Path "/api/v1/admin/templates/$($template.Id)" -Body $body
                    }
                    if ($null -ne $icon) {
                        Send-PaTemplateIcon -Id $template.Id -Bytes $icon
                    } elseif ($RemoveIcon) {
                        $null = Invoke-PaApi -Method DELETE -Path "/api/v1/admin/templates/$($template.Id)/icon"
                    }
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
