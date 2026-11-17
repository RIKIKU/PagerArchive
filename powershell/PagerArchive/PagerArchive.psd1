@{
    RootModule           = 'PagerArchive.psm1'
    ModuleVersion        = '0.17.0'
    GUID                 = '5e2fd431-890a-492d-8fc8-f7217b3bb42d'
    Author               = 'PagerArchive contributors'
    CompanyName          = 'PagerArchive'
    Copyright            = '(c) PagerArchive contributors. MIT licence.'
    Description          = 'Manage a PagerArchive server from PowerShell: sign in with your PagerArchive account, manage templates and aliases, import aliases through the pipeline, search, watch and export messages, back up the server now, and see, pause, resume, stop or start again its re-parse of stored messages.'
    CompatiblePSEditions = @('Core')
    PowerShellVersion    = '7.6'
    FormatsToProcess     = @('PagerArchive.Format.ps1xml')
    TypesToProcess       = @('PagerArchive.Types.ps1xml')
    FunctionsToExport    = @(
        'Connect-PagerArchive'
        'Disconnect-PagerArchive'
        'Export-PagerArchiveCsv'
        'Get-PagerArchiveAlias'
        'Get-PagerArchiveBackup'
        'Get-PagerArchiveMessage'
        'Get-PagerArchiveReparse'
        'Get-PagerArchiveTemplate'
        'Import-PagerArchiveAlias'
        'New-PagerArchiveAlias'
        'New-PagerArchiveTemplate'
        'Remove-PagerArchiveAlias'
        'Remove-PagerArchiveTemplate'
        'Rename-PagerArchiveAlias'
        'Rename-PagerArchiveTemplate'
        'Resume-PagerArchiveReparse'
        'Set-PagerArchiveAlias'
        'Set-PagerArchiveTemplate'
        'Start-PagerArchiveBackup'
        'Start-PagerArchiveReparse'
        'Stop-PagerArchiveReparse'
        'Suspend-PagerArchiveReparse'
        'Watch-PagerArchiveMessage'
    )
    AliasesToExport      = @(
        'Connect-Pa'
        'Disconnect-Pa'
        'Export-PaCsv'
        'Get-PaAlias'
        'Get-PaBackup'
        'Get-PaMessage'
        'Get-PaReparse'
        'Get-PaTemplate'
        'Import-PaAlias'
        'New-PaAlias'
        'New-PaTemplate'
        'Remove-PaAlias'
        'Remove-PaTemplate'
        'Rename-PaAlias'
        'Rename-PaTemplate'
        'Resume-PaReparse'
        'Set-PaAlias'
        'Set-PaTemplate'
        'Start-PaBackup'
        'Start-PaReparse'
        'Stop-PaReparse'
        'Suspend-PaReparse'
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
