@{
    RootModule           = 'PagerArchive.psm1'
    ModuleVersion        = '0.9.0'
    GUID                 = '5e2fd431-890a-492d-8fc8-f7217b3bb42d'
    Author               = 'PagerArchive contributors'
    CompanyName          = 'PagerArchive'
    Copyright            = '(c) PagerArchive contributors. MIT licence.'
    Description          = 'Manage a PagerArchive server from PowerShell: sign in with your PagerArchive account, manage templates and aliases, import aliases through the pipeline, and search and watch messages.'
    CompatiblePSEditions = @('Core')
    PowerShellVersion    = '7.6'
    FormatsToProcess     = @('PagerArchive.Format.ps1xml')
    TypesToProcess       = @('PagerArchive.Types.ps1xml')
    FunctionsToExport    = @(
        'Connect-PagerArchive'
        'Disconnect-PagerArchive'
        'Get-PagerArchiveAlias'
        'Get-PagerArchiveMessage'
        'Get-PagerArchiveTemplate'
        'Import-PagerArchiveAlias'
        'New-PagerArchiveAlias'
        'New-PagerArchiveTemplate'
        'Remove-PagerArchiveAlias'
        'Remove-PagerArchiveTemplate'
        'Rename-PagerArchiveAlias'
        'Set-PagerArchiveAlias'
        'Set-PagerArchiveTemplate'
        'Watch-PagerArchiveMessage'
    )
    AliasesToExport      = @(
        'Connect-Pa'
        'Disconnect-Pa'
        'Get-PaAlias'
        'Get-PaMessage'
        'Get-PaTemplate'
        'Import-PaAlias'
        'New-PaAlias'
        'New-PaTemplate'
        'Remove-PaAlias'
        'Remove-PaTemplate'
        'Rename-PaAlias'
        'Set-PaAlias'
        'Set-PaTemplate'
        'Watch-PaMessage'
    )
    CmdletsToExport      = @()
    VariablesToExport    = @()
    PrivateData          = @{
        PSData = @{
            Tags       = @('PagerArchive', 'POCSAG', 'Paging', 'Archive')
            LicenseUri = 'https://github.com/RIKIKU/PagerArchive/blob/main/LICENSE'
            ProjectUri = 'https://github.com/RIKIKU/PagerArchive'
        }
    }
}
