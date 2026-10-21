# Authentik's token, device and revocation endpoints (ADR 0009 section 6).
# These are the only requests whose bodies carry a token, so they're sent
# with HttpClient: the form is built as bytes, with tokens copied straight
# out of their SecureStrings, and the reply's token values are copied
# straight into new SecureStrings. Neither ever becomes a .NET string here,
# and the byte buffers are zeroed afterwards.

# Fields of a reply that hold credentials.
$script:SecretFields = @('access_token', 'refresh_token', 'id_token', 'device_code')

function Add-PaFormValue {
    <#
    .SYNOPSIS
    Appends one form value to a byte list, percent-encoded.
    #>
    [CmdletBinding()]
    param(
        # The list to append to.
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [System.Collections.Generic.List[byte]] $Bytes,

        # The value: a string, or a SecureString that's never made a string.
        [Parameter(Mandatory)]
        [object] $Value
    )
    $unreserved = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~'
    if ($Value -is [securestring]) {
        $pointer = [System.Runtime.InteropServices.Marshal]::SecureStringToGlobalAllocUnicode($Value)
        try {
            for ($i = 0; $i -lt $Value.Length; $i++) {
                $c = [System.Runtime.InteropServices.Marshal]::ReadInt16($pointer, $i * 2)
                if ($c -lt 128 -and $unreserved.IndexOf([char]$c) -ge 0) {
                    $Bytes.Add([byte]$c)
                } else {
                    foreach ($b in [System.Text.Encoding]::UTF8.GetBytes([char]$c)) {
                        foreach ($e in [System.Text.Encoding]::ASCII.GetBytes(('%{0:X2}' -f $b))) {
                            $Bytes.Add($e)
                        }
                    }
                }
            }
            $c = 0
        } finally {
            [System.Runtime.InteropServices.Marshal]::ZeroFreeGlobalAllocUnicode($pointer)
        }
    } else {
        $Bytes.AddRange([System.Text.Encoding]::ASCII.GetBytes([uri]::EscapeDataString([string]$Value)))
    }
}

function Read-PaSecretField {
    <#
    .SYNOPSIS
    Copies a JSON string field out of a reply into a SecureString, and blanks it in the buffer.

    .DESCRIPTION
    Finds "name": "value" in the reply's bytes and decodes the value's JSON
    escapes (Authentik's device codes include punctuation such as " and \)
    as it copies it. Token values are ASCII, so other bytes are refused.
    Every byte of the value, escapes included, is overwritten. Returns $null
    when the field isn't there.
    #>
    [CmdletBinding()]
    [OutputType([securestring])]
    param(
        # The reply's bytes; the value is overwritten in place.
        [Parameter(Mandatory)]
        [byte[]] $Buffer,

        # The field's name.
        [Parameter(Mandatory)]
        [string] $Name
    )
    $needle = [System.Text.Encoding]::ASCII.GetBytes('"' + $Name + '"')
    $limit = $Buffer.Length - $needle.Length
    for ($start = 0; $start -le $limit; $start++) {
        $found = $true
        for ($j = 0; $j -lt $needle.Length; $j++) {
            if ($Buffer[$start + $j] -ne $needle[$j]) {
                $found = $false
                break
            }
        }
        if (-not $found) {
            continue
        }
        $i = $start + $needle.Length
        while ($i -lt $Buffer.Length -and $Buffer[$i] -in 9, 10, 13, 32) { $i++ }
        if ($i -ge $Buffer.Length -or $Buffer[$i] -ne 58) { continue }  # :
        $i++
        while ($i -lt $Buffer.Length -and $Buffer[$i] -in 9, 10, 13, 32) { $i++ }
        if ($i -ge $Buffer.Length -or $Buffer[$i] -ne 34) { return $null }  # " (null or another type)
        $i++
        $secure = [securestring]::new()
        $escapes = @{ 34 = 34; 92 = 92; 47 = 47; 98 = 8; 102 = 12; 110 = 10; 114 = 13; 116 = 9 }
        while ($i -lt $Buffer.Length -and $Buffer[$i] -ne 34) {
            $b = $Buffer[$i]
            $Buffer[$i] = 120  # x
            $i++
            if ($b -ge 128) {
                $secure.Dispose()
                throw (New-PaErrorRecord -ErrorId 'token_reply_unreadable' -Message "Authentik's reply couldn't be read." -Category InvalidData)
            }
            if ($b -ne 92) {
                $secure.AppendChar([char]$b)
                continue
            }
            # An escape: \" \\ \/ \b \f \n \r \t or \uXXXX.
            $e = if ($i -lt $Buffer.Length) { $Buffer[$i] } else { 0 }
            if ($e -ne 0) { $Buffer[$i] = 120 }
            $i++
            if ($escapes.ContainsKey([int]$e)) {
                $secure.AppendChar([char]$escapes[[int]$e])
            } elseif ($e -eq 117 -and $i + 4 -le $Buffer.Length) {
                $code = 0
                for ($k = 0; $k -lt 4; $k++) {
                    $code = $code * 16 + [System.Convert]::ToInt32([string][char]$Buffer[$i], 16)
                    $Buffer[$i] = 120
                    $i++
                }
                $secure.AppendChar([char]$code)
                $code = 0
            } else {
                $secure.Dispose()
                throw (New-PaErrorRecord -ErrorId 'token_reply_unreadable' -Message "Authentik's reply couldn't be read." -Category InvalidData)
            }
            $e = 0
        }
        $b = 0
        $secure.MakeReadOnly()
        return $secure
    }
    return $null
}

function Invoke-PaTokenRequest {
    <#
    .SYNOPSIS
    Posts a form to one of Authentik's OAuth endpoints and reads the reply safely.

    .DESCRIPTION
    Returns the reply's fields: credentials (access_token, refresh_token,
    id_token, device_code) as SecureStrings, everything else as read by
    ConvertFrom-Json, plus Status. A reply that isn't JSON (revocation
    answers with an empty body) gives just Status.
    #>
    [CmdletBinding()]
    [OutputType([hashtable])]
    param(
        # The endpoint's URL.
        [Parameter(Mandatory)]
        [uri] $Uri,

        # The form: names and values, strings or SecureStrings.
        [Parameter(Mandatory)]
        [System.Collections.IDictionary] $Form
    )
    $bytes = [System.Collections.Generic.List[byte]]::new()
    $first = $true
    foreach ($key in $Form.Keys) {
        if (-not $first) { $bytes.Add(38) }  # &
        $first = $false
        Add-PaFormValue -Bytes $bytes -Value $key
        $bytes.Add(61)  # =
        Add-PaFormValue -Bytes $bytes -Value $Form[$key]
    }
    $body = $bytes.ToArray()
    $bytes.Clear()
    $reply = $null
    $client = [System.Net.Http.HttpClient]::new()
    $client.Timeout = [timespan]::FromSeconds(30)
    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Post, $Uri)
    try {
        $request.Content = [System.Net.Http.ByteArrayContent]::new($body)
        $request.Content.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::new('application/x-www-form-urlencoded')
        $request.Headers.Accept.ParseAdd('application/json')
        Write-Verbose -Message "POST $($Uri.AbsolutePath)"
        try {
            $response = $client.SendAsync($request).GetAwaiter().GetResult()
        } catch {
            throw (New-PaErrorRecord -ErrorId 'authentik_unreachable' -Message "Authentik can't be reached at $($Uri.GetLeftPart([System.UriPartial]::Authority)): $($_.Exception.GetBaseException().Message)" -Category ConnectionError)
        }
        $reply = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
        $out = @{ Status = [int]$response.StatusCode }
        foreach ($field in $script:SecretFields) {
            $value = Read-PaSecretField -Buffer $reply -Name $field
            if ($null -ne $value) {
                $out[$field] = $value
            }
        }
        # Every credential is now blanked, so the rest can be read as text.
        $text = [System.Text.Encoding]::UTF8.GetString($reply)
        if ($text.TrimStart().StartsWith('{')) {
            $json = $text | ConvertFrom-Json -AsHashtable
            foreach ($key in $json.Keys) {
                if ($key -notin $script:SecretFields) {
                    $out[$key] = $json[$key]
                }
            }
        }
        $out
    } finally {
        [array]::Clear($body)
        if ($null -ne $reply) {
            [array]::Clear($reply)
        }
        $request.Dispose()
        $client.Dispose()
    }
}
