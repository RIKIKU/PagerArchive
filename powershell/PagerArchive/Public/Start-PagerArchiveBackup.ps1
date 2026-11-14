function Start-PagerArchiveBackup {
    <#
    .SYNOPSIS
    Backs up the server now: the same backup and check as the nightly one.

    .DESCRIPTION
    Asks the server's backup service for a backup now, as Back up now on
    Administration, Backups does (R86a). It dumps PagerArchive's and
    Authentik's databases, restores the dumps into a throwaway database and
    compares them, as each night's backup does, and it's kept with the
    nightly backups, replacing any made earlier the same day. Use it before
    a change you might want to undo, such as an upgrade.

    The backup service starts it within a minute. On its own the command
    returns once the server has the request; -Wait waits for the backup and
    its check to finish, and writes an error if it failed. Asks for
    confirmation with -Confirm, and -WhatIf asks for nothing.

    One at a time: while a backup is asked for or running, from here or the
    browser, the server refuses another (backup_queued, backup_running), and
    says since when.

    .EXAMPLE
    Start-PagerArchiveBackup

    Asks for a backup and returns at once. Get-PagerArchiveBackup shows how
    it's going.

    .EXAMPLE
    Start-PagerArchiveBackup -Wait -PassThru

    State Run  LastGoodAt          LastRunAt           Nights Error
    ----- ---  ----------          ---------           ------ -----
    ok    idle 14/11/2026 10:04:51 14/11/2026 10:04:51     14

    Waits until the backup and its check are done, then outputs the state.

    .EXAMPLE
    Start-PagerArchiveBackup -Wait -Timeout (New-TimeSpan -Hours 2) -ErrorAction Stop

    Waits up to two hours, for a large archive on a slow disk, and stops the
    script if the backup failed or isn't done by then.

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Backup with -PassThru: the backups' state once the
    server has the request (Run is queued), or with -Wait, once the backup is
    done. Its properties are Get-PagerArchiveBackup's.

    .LINK
    Get-PagerArchiveBackup

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Backup')]
    param(
        # Wait for the backup and its check to finish, showing progress, and
        # write an error if it failed or -Timeout passes. Off by default:
        # the command returns once the server has the request.
        [switch] $Wait,

        # How long -Wait waits. When it passes, the command writes an error
        # (backup_timeout), and the backup carries on. Default: 30 minutes.
        [PSDefaultValue(Help = '30 minutes')]
        [ValidateScript({ $_ -gt [timespan]::Zero })]
        [timespan] $Timeout = [timespan]::FromMinutes(30),

        # Output the backups' state. Off by default.
        [switch] $PassThru
    )
    if (-not $PSCmdlet.ShouldProcess("PagerArchive's and Authentik's databases", 'Back up now')) {
        return
    }
    try {
        $status = Invoke-PaApi -Method POST -Path '/api/v1/admin/backup' | ConvertTo-PaBackup
    } catch {
        # Not signed in, or no server to ask, stops the command; a refusal
        # (one already asked for or running) is an ordinary error.
        $record = Copy-PaErrorRecord -ErrorRecord $_
        if ($record.CategoryInfo.Category -in @('AuthenticationError', 'ConnectionError') -or $record.Exception.Data['PagerArchive.Code'] -eq 'several_servers') {
            $PSCmdlet.ThrowTerminatingError($record)
        }
        $PSCmdlet.WriteError($record)
        return
    }
    if (-not $Wait) {
        if ($PassThru) {
            $status
        }
        return
    }

    $asked = $status.RequestedAt
    $deadline = [datetimeoffset]::UtcNow + $Timeout
    $activity = 'Backing up PagerArchive'
    try {
        while ($status.Run -in @('queued', 'running')) {
            if ([datetimeoffset]::UtcNow -ge $deadline) {
                $late = @{
                    ErrorId      = 'backup_timeout'
                    Category     = 'OperationTimeout'
                    TargetObject = $status
                    Message      = "The backup isn't done after $($Timeout.ToString()). It carries on: Get-PagerArchiveBackup shows how it goes."
                }
                $PSCmdlet.WriteError((New-PaErrorRecord @late))
                if ($PassThru) {
                    $status
                }
                return
            }
            $doing = if ($status.Run -eq 'running' -and $status.RunningSince) {
                "Running since $($status.RunningSince.LocalDateTime.ToString('t')): the dumps, then the check"
            } else {
                'Asked for: the backup service starts it within a minute'
            }
            Write-Progress -Activity $activity -Status $doing
            Start-Sleep -Duration $script:BackupPollInterval
            $status = Invoke-PaApi -Method GET -Path '/api/v1/admin/backup' | ConvertTo-PaBackup
        }
    } catch {
        $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_))
        return
    } finally {
        Write-Progress -Activity $activity -Completed
    }

    $ours = $status.LastRunAt -and (-not $asked -or $status.LastRunAt -ge $asked)
    $why = if (-not $ours) {
        @{ ErrorId = 'backup_no_result'; Message = 'The backup service finished without a result for this backup. Get-PagerArchiveBackup shows the last one.' }
    } elseif ($status.State -eq 'failed') {
        @{ ErrorId = 'backup_failed'; Message = "The backup failed: $($status.Error)." }
    }
    if ($why) {
        $PSCmdlet.WriteError((New-PaErrorRecord @why -Category InvalidResult -TargetObject $status))
    }
    if ($PassThru) {
        $status
    }
}
