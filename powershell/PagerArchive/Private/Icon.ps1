# Template icons (R65, ADR 0011). The module reads an icon from a file, or
# downloads it from a URL on the admin's own machine, then uploads the
# bytes. PagerArchive's server never fetches anything from another site, and
# it checks and converts every icon itself.

# The largest icon file the API accepts: 100 KB.
$script:PaIconMaxBytes = 100KB

# How long downloading an icon may take, from the request to its last byte.
$script:PaIconDownloadTimeout = [timespan]::FromSeconds(30)

function New-PaIconTooLarge {
    <#
    .SYNOPSIS
    The refusal for an icon over 100 KB, worded as the API's.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Makes an object in memory; changes nothing.')]
    [CmdletBinding()]
    [OutputType([System.Management.Automation.ErrorRecord])]
    param(
        # Where the icon came from, for the message.
        [Parameter(Mandatory)]
        [string] $Source
    )
    New-PaErrorRecord -ErrorId 'icon_too_large' -Status 413 -Message "An icon file can be at most 100 KB, and $Source is larger. Make it smaller (64x64 pixels is enough) and try again."
}

function Get-PaIconData {
    <#
    .SYNOPSIS
    An icon's bytes, from a file (-Path or -LiteralPath) or a URL (-Uri).

    .DESCRIPTION
    A file over 100 KB is refused before it's read. A download is refused
    as soon as it passes 100 KB, or takes longer than 30 seconds. Nothing
    else is checked here: the API checks and converts every icon.
    #>
    [CmdletBinding()]
    [OutputType([byte[]])]
    param(
        # A file path that may hold wildcards; it must match one file.
        [string] $Path,

        # A file path taken literally.
        [string] $LiteralPath,

        # An http or https address.
        [uri] $Uri
    )
    if ($PSBoundParameters.ContainsKey('Uri')) {
        return Receive-PaIcon -Uri $Uri
    }
    $found = if ($PSBoundParameters.ContainsKey('LiteralPath')) {
        @(Resolve-Path -LiteralPath $LiteralPath -ErrorAction SilentlyContinue)
    } else {
        @(Resolve-Path -Path $Path -ErrorAction SilentlyContinue)
    }
    $given = if ($PSBoundParameters.ContainsKey('LiteralPath')) { $LiteralPath } else { $Path }
    $files = @($found | Where-Object -FilterScript { $_.Provider.Name -eq 'FileSystem' -and (Test-Path -LiteralPath $_.ProviderPath -PathType Leaf) })
    if ($files.Count -eq 0) {
        throw (New-PaErrorRecord -ErrorId 'icon_not_found' -Category ObjectNotFound -Message "No file was found at '$given'.")
    }
    if ($files.Count -gt 1) {
        throw (New-PaErrorRecord -ErrorId 'icon_path_ambiguous' -Category InvalidArgument -Message "'$given' matches $($files.Count) files. Give one icon file.")
    }
    $file = [System.IO.FileInfo]::new($files[0].ProviderPath)
    if ($file.Length -gt $script:PaIconMaxBytes) {
        throw (New-PaIconTooLarge -Source $file.Name)
    }
    Write-Verbose -Message "Reading the icon from $($file.Name)"
    Write-Output -InputObject ([System.IO.File]::ReadAllBytes($file.FullName)) -NoEnumerate
}

function Receive-PaIcon {
    <#
    .SYNOPSIS
    Downloads an icon on this machine, at most 100 KB within 30 seconds.

    .DESCRIPTION
    Sends no credentials and no cookies: never the PagerArchive sign-in.
    Uses the system's proxy settings. Stops reading as soon as the reply
    passes 100 KB, and refuses a reply that says it's larger before reading
    any of it. Waits in slices of 200 ms, so Ctrl+C stops it.
    #>
    [CmdletBinding()]
    [OutputType([byte[]])]
    param(
        # An http or https address.
        [Parameter(Mandatory)]
        [uri] $Uri
    )
    if (-not $Uri.IsAbsoluteUri -or $Uri.Scheme -notin @('http', 'https')) {
        throw (New-PaErrorRecord -ErrorId 'icon_uri' -Category InvalidArgument -Message "-Uri must be the http or https address of an image, not '$Uri'.")
    }
    Write-Verbose -Message "Downloading the icon from $($Uri.Host)"
    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.UseCookies = $false
    $handler.UseDefaultCredentials = $false
    $handler.MaxAutomaticRedirections = 5
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [System.Threading.Timeout]::InfiniteTimeSpan
    $cancel = [System.Threading.CancellationTokenSource]::new($script:PaIconDownloadTimeout)
    $response = $null
    $stream = $null
    try {
        $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Get, $Uri)
        $response = Wait-PaIconTask -Task $client.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cancel.Token) -Uri $Uri -Cancel $cancel
        if (-not $response.IsSuccessStatusCode) {
            throw (New-PaErrorRecord -ErrorId 'icon_download_failed' -Category ConnectionError -Message "Downloading the icon from $Uri failed: the site answered $([int]$response.StatusCode) $($response.ReasonPhrase).")
        }
        $declared = $response.Content.Headers.ContentLength
        if ($null -ne $declared -and $declared -gt $script:PaIconMaxBytes) {
            throw (New-PaIconTooLarge -Source "the file at $Uri")
        }
        $stream = Wait-PaIconTask -Task $response.Content.ReadAsStreamAsync($cancel.Token) -Uri $Uri -Cancel $cancel
        $bytes = [System.IO.MemoryStream]::new()
        $chunk = [byte[]]::new(16KB)
        while ($true) {
            $n = Wait-PaIconTask -Task $stream.ReadAsync($chunk, 0, $chunk.Length, $cancel.Token) -Uri $Uri -Cancel $cancel
            if ($n -eq 0) {
                break
            }
            $bytes.Write($chunk, 0, $n)
            if ($bytes.Length -gt $script:PaIconMaxBytes) {
                throw (New-PaIconTooLarge -Source "the file at $Uri")
            }
        }
        Write-Debug -Message "Downloaded $($bytes.Length) bytes"
        Write-Output -InputObject $bytes.ToArray() -NoEnumerate
    } finally {
        if ($stream) { $stream.Dispose() }
        if ($response) { $response.Dispose() }
        $client.Dispose()
        $cancel.Dispose()
    }
}

function Wait-PaIconTask {
    <#
    .SYNOPSIS
    A download task's result, waiting in 200 ms slices so Ctrl+C works.
    #>
    [CmdletBinding()]
    [OutputType([object])]
    param(
        # The task.
        [Parameter(Mandatory)]
        [System.Threading.Tasks.Task] $Task,

        # The address, for the messages.
        [Parameter(Mandatory)]
        [uri] $Uri,

        # The download's time limit.
        [Parameter(Mandatory)]
        [System.Threading.CancellationTokenSource] $Cancel
    )
    try {
        while (-not $Task.Wait(200)) {
        }
    } catch {
        if ($Cancel.IsCancellationRequested) {
            throw (New-PaErrorRecord -ErrorId 'icon_download_timeout' -Category OperationTimeout -Message "Downloading the icon from $Uri took longer than $([int]$script:PaIconDownloadTimeout.TotalSeconds) seconds, so it was stopped.")
        }
        throw (New-PaErrorRecord -ErrorId 'icon_download_failed' -Category ConnectionError -Message "Downloading the icon from $Uri failed: $($_.Exception.GetBaseException().Message)")
    }
    $Task.Result
}

function Send-PaTemplateIcon {
    <#
    .SYNOPSIS
    Uploads a template's icon; the API checks and converts it.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Called by public commands after their ShouldProcess.')]
    [CmdletBinding()]
    [OutputType([void])]
    param(
        # The template's ID.
        [Parameter(Mandatory)]
        [long] $Id,

        # The icon file's bytes.
        [Parameter(Mandatory)]
        [byte[]] $Bytes
    )
    $null = Invoke-PaApi -Method PUT -Path "/api/v1/admin/templates/$Id/icon" -RawBody $Bytes
}
