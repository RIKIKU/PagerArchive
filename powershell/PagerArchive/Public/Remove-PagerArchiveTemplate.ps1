function Remove-PagerArchiveTemplate {
    <#
    .SYNOPSIS
    Deletes templates that no alias uses.

    .DESCRIPTION
    Deletes templates. A template still used by aliases, or named by a
    waiting alias request, can't be deleted, and the error says how many use
    it (R64). Asks for confirmation unless -Confirm:$false is given.

    .EXAMPLE
    Remove-PagerArchiveTemplate -Name 'Example Rescue'

    .EXAMPLE
    Get-PagerArchiveTemplate | Where-Object -Property AliasCount -EQ 0 | Remove-PagerArchiveTemplate -Confirm:$false

    Deletes every template no alias uses.

    .INPUTS
    System.String. Template names, by value or by the Name property, such
    as PagerArchive.Template objects.

    .OUTPUTS
    None, or PagerArchive.Template (the deleted template) with -PassThru.

    .LINK
    Get-PagerArchiveTemplate

    .LINK
    Set-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High')]
    [OutputType('PagerArchive.Template')]
    param(
        # The templates to delete. Accepts wildcards, matched
        # case-insensitively.
        [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

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
        foreach ($n in $Name) {
            $wild = [System.Management.Automation.WildcardPattern]::new($n, [System.Management.Automation.WildcardOptions]::IgnoreCase)
            $found = @($templates | Where-Object -FilterScript { $wild.IsMatch($_.Name) })
            if (-not $found -and -not [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($n)) {
                $PSCmdlet.WriteError((New-PaErrorRecord -ErrorId 'no_template' -Category ObjectNotFound -Message "No template is called '$n'." -TargetObject $n))
            }
            foreach ($template in $found) {
                if (-not $PSCmdlet.ShouldProcess($template.Name, 'Delete template')) {
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
