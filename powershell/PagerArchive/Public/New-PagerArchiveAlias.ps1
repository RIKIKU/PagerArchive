function New-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Adds an alias.

    .DESCRIPTION
    Adds an alias for a capcode, or for a pattern of capcodes with % (any
    run of characters) and _ (one character), with a name and, optionally,
    a template (R27, R53). Each capcode or pattern has one alias: adding a
    second is refused, naming the existing one (R27a). Its pages are
    re-parsed in the background, and the change is recorded in the alias's
    history as yours (R57).

    .EXAMPLE
    New-PagerArchiveAlias -Pattern 0100005 -Name 'Station 5' -Template 'Example Fire'

    .EXAMPLE
    New-PagerArchiveAlias -Pattern '0500%' -Name 'Network test pages' -Ignored

    Pages to 0500000-0599999 are stored and shown in full, but hidden from
    the default message list.

    .INPUTS
    System.Management.Automation.PSObject. Objects with Pattern, Name,
    Template and Ignored properties.

    .OUTPUTS
    PagerArchive.Alias. The alias added.

    .LINK
    Get-PagerArchiveAlias

    .LINK
    Import-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Alias')]
    param(
        # The capcode (1 to 7 digits), or a pattern with % and _. No
        # PowerShell wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
        [Alias('Capcode')]
        [ValidateNotNullOrEmpty()]
        [string] $Pattern,

        # The alias's name, 1 to 100 characters. No wildcards.
        [Parameter(Mandatory, Position = 1, ValueFromPipelineByPropertyName)]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The template's name. Default: none (text only). No wildcards.
        [Parameter(ValueFromPipelineByPropertyName)]
        [AllowEmptyString()]
        [string] $Template,

        # Hide its pages from the default message list (R27). Off by
        # default.
        [Parameter(ValueFromPipelineByPropertyName)]
        [PagerArchiveBoolTransformation()]
        [switch] $Ignored
    )
    begin {
        $templates = $null
    }
    process {
        if (-not $PSCmdlet.ShouldProcess("$Pattern ($Name)", 'Add alias')) {
            return
        }
        try {
            $templateId = $null
            if ($Template) {
                if ($null -eq $templates) { $templates = @(Get-PaTemplateList) }
                $templateId = (Find-PaTemplate -Name $Template -From $templates).Id
            }
            $body = @{ pattern = $Pattern; name = $Name; template_id = $templateId; ignored = [bool]$Ignored }
            $created = Invoke-PaApi -Method POST -Path '/api/v1/admin/aliases' -Body $body
            Get-PaAliasList -Search $Name | Where-Object -FilterScript { $_.Id -eq $created.id }
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $Pattern))
        }
    }
}
