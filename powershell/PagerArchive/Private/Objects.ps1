# Objects the commands output, and the lookups they share.

function ConvertTo-PaAlias {
    <#
    .SYNOPSIS
    A PagerArchive.Alias from the API's alias row.

    .DESCRIPTION
    Its properties are the import's parameters (guidelines section 3), so
    Get-PagerArchiveAlias | Export-Csv writes the import format.
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Alias')]
    param(
        # The API's row.
        [Parameter(Mandatory, ValueFromPipeline)]
        [object] $Row
    )
    process {
        [pscustomobject]@{
            PSTypeName = 'PagerArchive.Alias'
            Pattern    = $Row.pattern
            Name       = $Row.name
            Template   = if ($Row.template) { $Row.template } else { '' }
            Colour     = if ($Row.colour) { $Row.colour } else { '' }
            Ignored    = [bool]$Row.ignored
            Id         = [long]$Row.id
        }
    }
}

function ConvertTo-PaTemplate {
    <#
    .SYNOPSIS
    A PagerArchive.Template from the API's template row.
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Template')]
    param(
        # The API's row.
        [Parameter(Mandatory, ValueFromPipeline)]
        [object] $Row
    )
    process {
        [pscustomobject]@{
            PSTypeName    = 'PagerArchive.Template'
            Name          = $Row.name
            Agency        = $Row.agency
            Colour        = if ($Row.colour) { $Row.colour } else { '' }
            AgencyProfile = if ($Row.agency_profile) { $Row.agency_profile } else { '' }
            AliasCount    = [int]$Row.aliases
            Id            = [long]$Row.id
        }
    }
}

function ConvertTo-PaMessage {
    <#
    .SYNOPSIS
    A PagerArchive.Message from the API's message (R63a: what the detail view shows).
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Message')]
    param(
        # The API's message.
        [Parameter(Mandatory, ValueFromPipeline)]
        [object] $Row
    )
    process {
        $location = $null
        if ($Row.location) {
            $location = [pscustomobject]@{
                PSTypeName  = 'PagerArchive.Location'
                Longitude   = [double]$Row.location.lon
                Latitude    = [double]$Row.location.lat
                Approximate = [bool]$Row.location.approximate
                Source      = $Row.location.source
            }
        }
        $fields = [ordered]@{}
        if ($Row.extra) {
            foreach ($p in $Row.extra.PSObject.Properties) {
                $fields[$p.Name] = $p.Value
            }
        }
        $flags = $Row.PSObject.Properties['flags'] -and $Row.flags
        [pscustomobject]@{
            PSTypeName       = 'PagerArchive.Message'
            Id               = [long]$Row.id
            SentAt           = [datetimeoffset]$Row.sent_at
            Capcode          = $Row.capcode
            Alias            = $Row.alias
            Agency           = $Row.agency
            Priority         = if ($Row.priority) { $Row.priority } else { 'None' }
            Text             = $Row.display_text
            RawText          = $Row.text
            Group            = $Row.group
            JobNumber        = $Row.job_number
            EventType        = $Row.event_type
            Category         = $Row.category
            Description      = $Row.description
            Address          = $Row.address
            Locality         = $Row.locality
            GridRef          = $Row.grid_ref
            Units            = @($Row.units | Where-Object -FilterScript { $_ })
            Fields           = [pscustomobject]$fields
            Location         = $location
            ParseFailed      = if ($flags) { [bool]$Row.flags.parse_failed } else { $false }
            AddressUnsplit   = if ($flags) { [bool]$Row.flags.address_unsplit } else { $false }
            LocationMismatch = if ($flags) { [bool]$Row.flags.location_mismatch } else { $false }
            Heartbeat        = [bool]$Row.heartbeat
            Ignored          = [bool]$Row.ignored
            Receptions       = @(foreach ($r in @($Row.PSObject.Properties['receptions'] ? $Row.receptions : @())) {
                    [pscustomobject]@{
                        PSTypeName = 'PagerArchive.Reception'
                        Client     = $r.client
                        ReceivedAt = [datetimeoffset]$r.received_at
                    }
                })
        }
    }
}

function ConvertTo-PaLikePattern {
    <#
    .SYNOPSIS
    Turns a PowerShell wildcard into a case-insensitive LIKE pattern.

    .DESCRIPTION
    * becomes %, ? becomes _, and %, _ and \ are escaped, so an exact name
    matches only itself. A [set] becomes _ (any one character), and Exact is
    $false so the caller also checks the wildcard itself.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The wildcard.
        [Parameter(Mandatory)]
        [string] $Wildcard
    )
    $like = [System.Text.StringBuilder]::new()
    $exact = $true
    for ($i = 0; $i -lt $Wildcard.Length; $i++) {
        $c = $Wildcard[$i]
        switch ($c) {
            '`' {
                if ($i + 1 -lt $Wildcard.Length) {
                    $i++
                    $n = $Wildcard[$i]
                    if ($n -in [char]'%', [char]'_', [char]'\') { $null = $like.Append('\') }
                    $null = $like.Append($n)
                }
                break
            }
            '*' { $null = $like.Append('%'); break }
            '?' { $null = $like.Append('_'); break }
            '[' {
                $close = $Wildcard.IndexOf(']', $i + 1)
                if ($close -lt 0) {
                    $null = $like.Append('[')
                } else {
                    $null = $like.Append('_')
                    $exact = $false
                    $i = $close
                }
                break
            }
            { $_ -in [char]'%', [char]'_', [char]'\' } { $null = $like.Append('\').Append($c); break }
            default { $null = $like.Append($c) }
        }
    }
    @{ Like = $like.ToString(); Exact = $exact }
}

function Get-PaTemplateList {
    <#
    .SYNOPSIS
    Every template, from the API.
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Template')]
    param()
    Invoke-PaApi -Method GET -Path '/api/v1/admin/templates' | ForEach-Object -Process { $_ } | ConvertTo-PaTemplate
}

function Get-PaAliasList {
    <#
    .SYNOPSIS
    Aliases from the API, a page at a time, written as each page arrives.

    .DESCRIPTION
    Search narrows them on the server (a name, capcode or pattern
    containing it); callers filter exactly.
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Alias')]
    param(
        # Text the name, capcode or pattern contains.
        [string] $Search = ''
    )
    $after = ''
    do {
        $query = [ordered]@{ q = $Search; limit = 500; after = $after }
        $page = Invoke-PaApi -Method GET -Path '/api/v1/admin/aliases' -Query $query
        $rows = @($page.aliases)
        $rows | ConvertTo-PaAlias
        if ($rows.Count) {
            $after = $rows[-1].pattern
        }
    } while ($page.more -and $rows.Count)
}

function Find-PaTemplate {
    <#
    .SYNOPSIS
    The template called Name (case-insensitive), or an error.
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Template')]
    param(
        # The template's name.
        [Parameter(Mandatory)]
        [string] $Name,

        # Templates already fetched.
        [object[]] $From
    )
    if (-not $From) {
        $From = @(Get-PaTemplateList)
    }
    $found = $From | Where-Object -FilterScript { $_.Name -eq $Name } | Select-Object -First 1
    if (-not $found) {
        throw (New-PaErrorRecord -ErrorId 'no_template' -Category ObjectNotFound -Message "No template is called '$Name'. Get-PagerArchiveTemplate lists them." -TargetObject $Name)
    }
    $found
}

function Find-PaAlias {
    <#
    .SYNOPSIS
    The aliases a command acts on: by capcode or pattern, or by name with wildcards.
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Alias')]
    param(
        # Capcodes or patterns, exactly (zero-padded or not).
        [string[]] $Pattern,

        # Names, with wildcards.
        [string[]] $Name
    )
    foreach ($p in $Pattern) {
        $p = $p.Trim()
        $key = if ($p -match '^[0-9]{1,7}$') { $p.PadLeft(7, '0') } else { $p }
        $found = Get-PaAliasList -Search $key | Where-Object -FilterScript { $_.Pattern -eq $key }
        if (-not $found) {
            throw (New-PaErrorRecord -ErrorId 'no_alias' -Category ObjectNotFound -Message "No alias has the capcode or pattern '$p'." -TargetObject $p)
        }
        $found
    }
    if ($Name) {
        $all = $null
        foreach ($n in $Name) {
            $wild = [System.Management.Automation.WildcardPattern]::new($n, [System.Management.Automation.WildcardOptions]::IgnoreCase)
            if (-not [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($n)) {
                $found = Get-PaAliasList -Search $n | Where-Object -FilterScript { $wild.IsMatch($_.Name) }
            } else {
                if ($null -eq $all) { $all = @(Get-PaAliasList) }
                $found = $all | Where-Object -FilterScript { $wild.IsMatch($_.Name) }
            }
            if (-not $found -and -not [System.Management.Automation.WildcardPattern]::ContainsWildcardCharacters($n)) {
                throw (New-PaErrorRecord -ErrorId 'no_alias' -Category ObjectNotFound -Message "No alias is called '$n'." -TargetObject $n)
            }
            $found
        }
    }
}

function New-PaConnectionObject {
    <#
    .SYNOPSIS
    The PagerArchive.Connection object for a connection.
    #>
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSUseShouldProcessForStateChangingFunctions', '', Justification = 'Makes an object in memory; changes nothing.')]
    [CmdletBinding()]
    [OutputType('PagerArchive.Connection')]
    param(
        # The connection.
        [Parameter(Mandatory)]
        [hashtable] $Connection
    )
    $user = $Connection.User
    [pscustomobject]@{
        PSTypeName = 'PagerArchive.Connection'
        Uri        = [uri]$Connection.Uri
        Name       = if ($user -and $user.name) { $user.name } else { '' }
        Email      = if ($user) { $user.email } else { '' }
        Role       = if ($user) { $user.role } else { '' }
        SavedIn    = switch ($Connection.Store) {
            'dpapi' { 'DPAPI file' }
            'vault' { "Vault $($Connection.Vault)" }
            default { 'This session only' }
        }
        ExpiresAt  = $Connection.ExpiresAt
    }
}

function Get-PaImportSummary {
    <#
    .SYNOPSIS
    The import's one-line summary for its single prompt, e.g. "412 aliases: 380 new, 12 different".
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        # The dry run's report.
        [Parameter(Mandatory)]
        [object] $Report
    )
    $count = @{}
    foreach ($row in $Report.rows) {
        $count[$row.result] = 1 + $(if ($count.ContainsKey($row.result)) { $count[$row.result] } else { 0 })
    }
    $parts = foreach ($pair in @(@('added', 'new'), @('updated', 'to update'), @('different', 'different'), @('squashed', 'squashed'), @('unchanged', 'unchanged'), @('invalid', 'invalid (skipped)'))) {
        if ($count.ContainsKey($pair[0])) {
            "$($count[$pair[0]]) $($pair[1])"
        }
    }
    $total = @($Report.rows).Count
    $noun = if ($total -eq 1) { 'alias' } else { 'aliases' }
    $templates = @($Report.templates_created).Count
    $tail = if ($templates -gt 0) { "; $templates new template$(if ($templates -ne 1) { 's' })" } else { '' }
    "${total} ${noun}: $($parts -join ', ')$tail"
}