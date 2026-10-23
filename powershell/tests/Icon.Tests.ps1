#Requires -Modules @{ ModuleName = 'Pester'; RequiredVersion = '5.9.1' }
# Template icons in the module (R63, R65, ADR 0011), with the API replaced
# (Invoke-PaApi mocked): an icon from a file or a URL is uploaded as bytes,
# a URL is downloaded on this machine with a size and a time limit and no
# credentials, -RemoveIcon removes it, and -WhatIf touches nothing. Downloads
# go to a test web server on a real local socket, run in its own runspace.
# The made-up test icons are in testdata/icons. A58 runs against the real
# stack in internal/stacktest.

BeforeAll {
    Import-Module -Name (Join-Path -Path $PSScriptRoot -ChildPath '../PagerArchive/PagerArchive.psd1') -Force
    $script:icons = Join-Path -Path $PSScriptRoot -ChildPath '../../testdata/icons'
    $script:iconBytes = [System.IO.File]::ReadAllBytes((Join-Path -Path $script:icons -ChildPath 'example-icon.png'))

    function Split-Output([object[]] $All) {
        @{
            Out    = @($All | Where-Object -FilterScript { $_ -isnot [System.Management.Automation.ErrorRecord] })
            Errors = @($All | Where-Object -FilterScript { $_ -is [System.Management.Automation.ErrorRecord] })
        }
    }

    # A web server for downloads. Paths: /icon.png (the test icon),
    # /redirect (to /icon.png), /declared-too-big (says 200 KB), /chunked-too-big
    # (sends up to 32 MB without saying how much), /stall (sends the head
    # and a few bytes, then nothing), anything else 404. It records each
    # request's head and how many body bytes it sent.
    function Start-IconServer {
        $listener = [System.Net.Sockets.TcpListener]::new([ipaddress]::Loopback, 0)
        $listener.Start()
        $state = [hashtable]::Synchronized(@{ Stop = $false; Requests = [System.Collections.Generic.List[string]]::new(); Sent = 0 })
        $ps = [powershell]::Create()
        $null = $ps.AddScript({
                param($Listener, $State, [byte[]] $Icon)
                $ascii = [System.Text.Encoding]::ASCII
                while (-not $State.Stop) {
                    if (-not $Listener.Pending()) {
                        Start-Sleep -Milliseconds 10
                        continue
                    }
                    $client = $Listener.AcceptTcpClient()
                    try {
                        $stream = $client.GetStream()
                        $stream.ReadTimeout = 5000
                        $head = ''
                        $buffer = [byte[]]::new(4096)
                        while (-not $head.Contains("`r`n`r`n")) {
                            $n = $stream.Read($buffer, 0, $buffer.Length)
                            if ($n -eq 0) { break }
                            $head += $ascii.GetString($buffer, 0, $n)
                        }
                        $State.Requests.Add($head)
                        $path = ($head -split ' ')[1]
                        $send = { param([byte[]] $b) $stream.Write($b, 0, $b.Length); $stream.Flush() }
                        switch ($path) {
                            '/icon.png' {
                                & $send $ascii.GetBytes("HTTP/1.1 200 OK`r`nContent-Type: image/png`r`nContent-Length: $($Icon.Length)`r`nConnection: close`r`n`r`n")
                                & $send $Icon
                                $State.Sent += $Icon.Length
                            }
                            '/redirect' {
                                & $send $ascii.GetBytes("HTTP/1.1 302 Found`r`nLocation: /icon.png`r`nContent-Length: 0`r`nConnection: close`r`n`r`n")
                            }
                            '/declared-too-big' {
                                & $send $ascii.GetBytes("HTTP/1.1 200 OK`r`nContent-Type: image/png`r`nContent-Length: 204800`r`nConnection: close`r`n`r`n")
                                & $send ([byte[]]::new(1024))
                                $State.Sent += 1024
                            }
                            '/chunked-too-big' {
                                & $send $ascii.GetBytes("HTTP/1.1 200 OK`r`nContent-Type: image/png`r`nTransfer-Encoding: chunked`r`nConnection: close`r`n`r`n")
                                $chunk = [byte[]]::new(8192)
                                for ($i = 0; $i -lt 4096; $i++) {
                                    & $send ($ascii.GetBytes('2000' + "`r`n") + $chunk + [byte[]](13, 10))
                                    $State.Sent += $chunk.Length
                                }
                                & $send $ascii.GetBytes("0`r`n`r`n")
                            }
                            '/stall' {
                                & $send $ascii.GetBytes("HTTP/1.1 200 OK`r`nContent-Type: image/png`r`nContent-Length: 1000`r`nConnection: close`r`n`r`n")
                                & $send ([byte[]]::new(10))
                                $State.Sent += 10
                                $until = [datetime]::UtcNow.AddSeconds(15)
                                while (-not $State.Stop -and [datetime]::UtcNow -lt $until -and -not ($client.Client.Poll(0, [System.Net.Sockets.SelectMode]::SelectRead) -and $client.Available -eq 0)) { Start-Sleep -Milliseconds 50 }
                            }
                            default {
                                & $send $ascii.GetBytes("HTTP/1.1 404 Not Found`r`nContent-Length: 0`r`nConnection: close`r`n`r`n")
                            }
                        }
                    } catch {
                        # The module hung up: that's what some tests want.
                        $null = $_
                    } finally {
                        $client.Dispose()
                    }
                }
            }).AddArgument($listener).AddArgument($state).AddArgument($script:iconBytes)
        $handle = $ps.BeginInvoke()
        [pscustomobject]@{ Listener = $listener; State = $state; PowerShell = $ps; Handle = $handle; Url = "http://127.0.0.1:$($listener.LocalEndpoint.Port)" }
    }

    function Stop-IconServer($Server) {
        $Server.State.Stop = $true
        $null = $Server.Handle.AsyncWaitHandle.WaitOne(20000)
        $Server.PowerShell.Dispose()
        $Server.Listener.Stop()
    }
}

Describe 'Icons for templates' {
    BeforeAll {
        $script:server = Start-IconServer
    }
    AfterAll {
        Stop-IconServer $script:server
    }
    BeforeEach {
        $script:uploads = [System.Collections.Generic.List[object]]::new()
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Path -eq '/api/v1/admin/templates' -and $Method -eq 'GET' } -MockWith {
            @([pscustomobject]@{ id = 1; name = 'Example Fire'; agency = 'Example Fire Service'; colour = $null; agency_profile = 'example-fire'; icon = '/api/v1/icons/abc.png'; aliases = 4 }
                [pscustomobject]@{ id = 2; name = 'Example Rescue'; agency = 'Example Rescue Service'; colour = $null; agency_profile = $null; icon = $null; aliases = 0 })
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' -and $Path -eq '/api/v1/admin/templates' } -MockWith { [pscustomobject]@{ id = 2 } }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PUT' -and $Path -like '/api/v1/admin/templates/*/icon' } -MockWith {
            $script:uploads.Add(@{ Path = $Path; Bytes = $RawBody })
            [pscustomobject]@{ icon = '/api/v1/icons/def.png' }
        }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -like '/api/v1/admin/templates/*/icon' } -MockWith { $null }
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' } -MockWith { $null }
    }

    It 'Get-PagerArchiveTemplate shows whether a template has an icon' {
        $t = Get-PagerArchiveTemplate
        $t.HasIcon | Should -Be @($true, $false)
        ($t[0] | Format-Table | Out-String) | Should -Match 'HasIcon'
    }

    It 'Set-PagerArchiveTemplate -Path uploads the file''s bytes, as they are' {
        Set-PagerArchiveTemplate -Name 'Example Fire' -Path (Join-Path -Path $script:icons -ChildPath 'example-*.png')
        $script:uploads.Count | Should -Be 1
        $script:uploads[0].Path | Should -Be '/api/v1/admin/templates/1/icon'
        [System.Linq.Enumerable]::SequenceEqual([byte[]]$script:uploads[0].Bytes, [byte[]]$script:iconBytes) | Should -BeTrue
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' } -Times 0 -Exactly
    }

    It 'takes -LiteralPath exactly as written' {
        $file = Join-Path -Path $TestDrive -ChildPath 'icon[1].png'
        [System.IO.File]::WriteAllBytes($file, $script:iconBytes)
        Set-PagerArchiveTemplate -Name 'Example Fire' -LiteralPath $file
        $script:uploads.Count | Should -Be 1
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Path $file -ErrorAction Stop } | Should -Throw -ErrorId 'icon_not_found,Set-PagerArchiveTemplate'
    }

    It 'takes -PSPath, a provider path, literally (guidelines section 3)' {
        $file = Join-Path -Path $TestDrive -ChildPath 'icon[1].png'
        [System.IO.File]::WriteAllBytes($file, $script:iconBytes)
        $item = Get-Item -LiteralPath $file
        $item.PSPath | Should -BeLike '*::*icon`[1`].png'
        foreach ($command in 'Set-PagerArchiveTemplate', 'New-PagerArchiveTemplate') {
            (Get-Command -Name $command).Parameters['LiteralPath'].Aliases | Should -Be @('PSPath')
            (Get-Command -Name $command).Parameters['Path'].Aliases | Should -Not -Contain 'PSPath'
        }
        Set-PagerArchiveTemplate -Name 'Example Fire' -PSPath $item.PSPath
        New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -PSPath (Get-Item -LiteralPath $file).PSPath
        $script:uploads.Path | Should -Be @('/api/v1/admin/templates/1/icon', '/api/v1/admin/templates/2/icon')
        foreach ($upload in $script:uploads) {
            [System.Linq.Enumerable]::SequenceEqual([byte[]]$upload.Bytes, [byte[]]$script:iconBytes) | Should -BeTrue
        }
    }

    It 'refuses a -Path matching several files, before changing anything' {
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Colour red -Path (Join-Path -Path $script:icons -ChildPath '*.png') -ErrorAction Stop } |
            Should -Throw -ErrorId 'icon_path_ambiguous,Set-PagerArchiveTemplate'
        $script:uploads.Count | Should -Be 0
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PATCH' } -Times 0 -Exactly
    }

    It 'refuses a file over 100 KB without uploading it' {
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Path (Join-Path -Path $script:icons -ChildPath 'large-200kb.png') -ErrorAction Stop } |
            Should -Throw -ErrorId 'icon_too_large,Set-PagerArchiveTemplate'
        $script:uploads.Count | Should -Be 0
    }

    It 'downloads -Uri on this machine, with no credentials, and uploads the bytes' {
        $before = $script:server.State.Requests.Count
        Set-PagerArchiveTemplate -Name 'Example*' -Uri "$($script:server.Url)/redirect"
        $script:uploads.Path | Should -Be @('/api/v1/admin/templates/1/icon', '/api/v1/admin/templates/2/icon')
        foreach ($u in $script:uploads) {
            [System.Linq.Enumerable]::SequenceEqual([byte[]]$u.Bytes, [byte[]]$script:iconBytes) | Should -BeTrue
        }
        # Downloaded once for both templates (and followed the redirect).
        $heads = @($script:server.State.Requests)[$before..($script:server.State.Requests.Count - 1)]
        $heads.Count | Should -Be 2
        $heads[0] | Should -Match '^GET /redirect HTTP/1\.1'
        $heads[1] | Should -Match '^GET /icon\.png HTTP/1\.1'
        $heads | Should -Not -Match '(?im)^(Authorization|Cookie):'
    }

    It 'New-PagerArchiveTemplate -Uri adds the template, then its icon' {
        $t = New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -Uri "$($script:server.Url)/icon.png"
        $t.Name | Should -Be 'Example Rescue'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' } -Times 1 -Exactly
        $script:uploads.Path | Should -Be @('/api/v1/admin/templates/2/icon')
    }

    It 'adds nothing when the icon can''t be read or downloaded' {
        $split = Split-Output -All (New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -Uri "$($script:server.Url)/missing.png" 2>&1)
        $split.Out.Count | Should -Be 0
        $split.Errors[0].FullyQualifiedErrorId | Should -Be 'icon_download_failed,New-PagerArchiveTemplate'
        $split.Errors[0].Exception.Message | Should -Match '404 Not Found'
        $split = Split-Output -All (New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -LiteralPath (Join-Path -Path $TestDrive -ChildPath 'none.png') 2>&1)
        $split.Errors[0].FullyQualifiedErrorId | Should -Be 'icon_not_found,New-PagerArchiveTemplate'
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'POST' } -Times 0 -Exactly
    }

    It 'keeps the template and reports the API''s refusal when the icon is refused' {
        Mock -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'PUT' } -MockWith {
            throw (& (Get-Module PagerArchive) { New-PaErrorRecord -ErrorId 'icon_svg' -Status 422 -Message 'SVG icons aren''t accepted.' })
        }
        $split = Split-Output -All (New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -Path (Join-Path -Path $script:icons -ChildPath 'example.svg') 2>&1)
        $split.Out.Name | Should -Be 'Example Rescue'
        $split.Errors.Count | Should -Be 1
        $split.Errors[0].FullyQualifiedErrorId | Should -Be 'icon_svg,New-PagerArchiveTemplate'
        $split.Errors[0].CategoryInfo.Category | Should -Be 'InvalidArgument'
        $split.Errors[0].Exception.Message | Should -Be 'Template ''Example Rescue'' was added without an icon: SVG icons aren''t accepted.'
    }

    It 'refuses a download that says it''s over 100 KB before reading it' {
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Uri "$($script:server.Url)/declared-too-big" -ErrorAction Stop } |
            Should -Throw -ErrorId 'icon_too_large,Set-PagerArchiveTemplate'
        $script:uploads.Count | Should -Be 0
    }

    It 'stops a download as soon as it passes 100 KB' {
        $sent = $script:server.State.Sent
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Uri "$($script:server.Url)/chunked-too-big" -ErrorAction Stop } |
            Should -Throw -ErrorId 'icon_too_large,Set-PagerArchiveTemplate'
        $script:uploads.Count | Should -Be 0
        # The server couldn't send all of its 32 MB once the module hung up
        # (what socket buffers took is the slack).
        Start-Sleep -Milliseconds 500
        ($script:server.State.Sent - $sent) | Should -BeLessThan 16MB
    }

    It 'stops a download that takes longer than the time limit' {
        & (Get-Module PagerArchive) { $script:PaIconDownloadTimeout = [timespan]::FromSeconds(1) }
        try {
            $watch = [System.Diagnostics.Stopwatch]::StartNew()
            { Set-PagerArchiveTemplate -Name 'Example Fire' -Uri "$($script:server.Url)/stall" -ErrorAction Stop } |
                Should -Throw -ErrorId 'icon_download_timeout,Set-PagerArchiveTemplate'
            $watch.Elapsed.TotalSeconds | Should -BeLessThan 5
            $script:uploads.Count | Should -Be 0
        } finally {
            & (Get-Module PagerArchive) { $script:PaIconDownloadTimeout = [timespan]::FromSeconds(30) }
        }
    }

    It 'refuses an address that isn''t http or https, without fetching it' {
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Uri 'file:///etc/hostname' -ErrorAction Stop } |
            Should -Throw -ErrorId 'icon_uri,Set-PagerArchiveTemplate'
        $script:uploads.Count | Should -Be 0
    }

    It '-RemoveIcon removes it' {
        Set-PagerArchiveTemplate -Name 'Example Fire' -RemoveIcon
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -eq 'DELETE' -and $Path -eq '/api/v1/admin/templates/1/icon' } -Times 1 -Exactly
    }

    It '-WhatIf neither reads, downloads nor changes anything' {
        $before = $script:server.State.Requests.Count
        Set-PagerArchiveTemplate -Name 'Example*' -Uri "$($script:server.Url)/icon.png" -WhatIf
        Set-PagerArchiveTemplate -Name 'Example*' -RemoveIcon -WhatIf
        New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -Uri "$($script:server.Url)/icon.png" -WhatIf
        $script:server.State.Requests.Count | Should -Be $before
        Should -Invoke -ModuleName PagerArchive Invoke-PaApi -ParameterFilter { $Method -ne 'GET' } -Times 0 -Exactly
    }

    It 'takes one icon source at a time' {
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Path 'a.png' -Uri 'https://example.test/a.png' -ErrorAction Stop } | Should -Throw -ErrorId 'AmbiguousParameterSet,Set-PagerArchiveTemplate'
        { Set-PagerArchiveTemplate -Name 'Example Fire' -Path 'a.png' -RemoveIcon -ErrorAction Stop } | Should -Throw -ErrorId 'AmbiguousParameterSet,Set-PagerArchiveTemplate'
    }

    It 'shows no file contents or tokens in -Verbose and -Debug' {
        $all = Set-PagerArchiveTemplate -Name 'Example Fire' -Uri "$($script:server.Url)/icon.png" -Verbose -Debug 4>&1 5>&1 | Out-String
        $all | Should -Match 'Downloading the icon from 127\.0\.0\.1'
        $all | Should -Not -Match 'IHDR|Bearer'
    }
}
