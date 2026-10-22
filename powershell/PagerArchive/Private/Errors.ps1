function New-PaErrorRecord {
    <#
    .SYNOPSIS
    Makes an ErrorRecord for a refusal or failure (guidelines section 7).

    .DESCRIPTION
    The ErrorId is the API's error code, the message is the API's message,
    and the category comes from the HTTP status. Messages never hold a token,
    a page's text or an address.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Makes an object in memory; changes nothing.')]
    [CmdletBinding()]
    [OutputType([System.Management.Automation.ErrorRecord])]
    param(
        # The API's error code, or another stable identifier.
        [Parameter(Mandatory)]
        [string] $ErrorId,

        # The message to show.
        [Parameter(Mandatory)]
        [string] $Message,

        # The HTTP status, or 0 when the server couldn't be reached.
        [int] $Status = 0,

        # The category, when there's no HTTP status to take it from.
        [System.Management.Automation.ErrorCategory] $Category = [System.Management.Automation.ErrorCategory]::NotSpecified,

        # The input item the error is about.
        [object] $TargetObject
    )
    if ($Status -ne 0) {
        $Category = switch ($Status) {
            { $_ -in 400, 413, 422 } { 'InvalidArgument'; break }
            401 { 'AuthenticationError'; break }
            403 { 'PermissionDenied'; break }
            404 { 'ObjectNotFound'; break }
            409 {
                if ($ErrorId -in @('duplicate_alias', 'template_exists', 'already_exists')) { 'ResourceExists' } else { 'InvalidOperation' }
                break
            }
            429 { 'LimitsExceeded'; break }
            { $_ -ge 500 } { 'ConnectionError'; break }
            default { 'NotSpecified' }
        }
    }
    $exception = [System.InvalidOperationException]::new($Message)
    $exception.Data['PagerArchive.Status'] = $Status
    $exception.Data['PagerArchive.Code'] = $ErrorId
    [System.Management.Automation.ErrorRecord]::new($exception, $ErrorId, $Category, $TargetObject)
}

function Copy-PaErrorRecord {
    <#
    .SYNOPSIS
    Re-targets an error from a private function for a public command.

    .DESCRIPTION
    Keeps the error's code (as the ErrorId), message and category, and sets
    the input item the public command was working on, so the error reads as
    the public command's own.
    #>
    [CmdletBinding()]
    [OutputType([System.Management.Automation.ErrorRecord])]
    param(
        # The error to copy.
        [Parameter(Mandatory)]
        [System.Management.Automation.ErrorRecord] $ErrorRecord,

        # The input item the error is about.
        [object] $TargetObject
    )
    $code = $ErrorRecord.Exception.Data['PagerArchive.Code']
    if (-not $code) {
        return $ErrorRecord
    }
    [System.Management.Automation.ErrorRecord]::new($ErrorRecord.Exception, $code, $ErrorRecord.CategoryInfo.Category, $TargetObject)
}
