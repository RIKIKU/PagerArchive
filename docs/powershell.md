# The PagerArchive PowerShell module

Admins manage PagerArchive from a command line on their own machines with the **PagerArchive** PowerShell module (SPEC.md R59–R63a). It uses PagerArchive's API, the same one the browser uses, so the same rules apply, and alias changes are recorded in each alias's history as yours. PowerShell access is for admins.

`pagerarchive` on the server keeps only the jobs that need the server: `serve`, `validate-profile`, `client` and `version`.

## Install

The module needs **PowerShell 7.6 or later**, on Windows, Linux or macOS.

From the PowerShell Gallery, once a release has been published there:

```powershell
Install-PSResource -Name PagerArchive -Repository PSGallery
```

From a copy of this repository (a branch, or before a release), import it from the folder:

```powershell
Import-Module ./powershell/PagerArchive/PagerArchive.psd1
```

or copy `powershell/PagerArchive` into a folder on `$env:PSModulePath` (`$HOME\Documents\PowerShell\Modules` on Windows, `~/.local/share/powershell/Modules` on Linux) to have it load by name.

Every command has a short alias with `Pa` in place of `PagerArchive`, e.g. `Get-PaAlias`. `Get-Help about_PagerArchive` and `Get-Help <command> -Full` describe everything below.

## Signing in

```powershell
Connect-PagerArchive -Uri https://pager.example.org
```

Your browser opens at PagerArchive's normal sign-in (email and password). When you've signed in, it says you can close the tab. There are **no API keys**: the module signs in as you, with OAuth's sign-in for native apps (PKCE, and a redirect to a random port on `127.0.0.1` on your own machine). On a machine with no browser:

```powershell
Connect-PagerArchive -Uri https://pager.example.org -UseDeviceCode
```

shows a code to enter at Authentik's `/device` page on any device.

**Staying signed in.** The module renews its access automatically, and saves the sign-in so new PowerShell windows use it:

| Where | Saved in | Set-up |
|---|---|---|
| Windows | a file in `%LOCALAPPDATA%\PagerArchive\SignIns`, encrypted with Windows' data protection (DPAPI): only your Windows account on that machine can read it | none |
| Linux, macOS | your default [SecretManagement](https://learn.microsoft.com/powershell/utility-modules/secretmanagement/overview) vault | a registered vault |
| Linux, macOS with no vault | nowhere: the sign-in lasts for that PowerShell session, with a warning | none |
| anywhere, `-Vault <name>` | that SecretManagement vault | that vault |

To stay signed in on Linux or macOS, register a vault once:

```powershell
Install-PSResource -Name Microsoft.PowerShell.SecretManagement, Microsoft.PowerShell.SecretStore
Register-SecretVault -Name SecretStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault
```

SecretStore asks for its password when first used, and again after 15 minutes (`Set-SecretStoreConfiguration -PasswordTimeout` changes that; `Unlock-SecretStore` unlocks it). Prefer a longer timeout to `-Authentication None`, which leaves the vault's key readable by anyone with your files. Microsoft has marked SecretManagement and SecretStore "feature complete": they get security fixes but no new features.

A sign-in not used for **30 days** ends. `Disconnect-PagerArchive` revokes it in Authentik and deletes the saved copy. Deactivating, deleting or demoting someone in PagerArchive takes effect on their next command.

**Plain HTTP.** A copy on your own network without HTTPS needs `-AllowUnencryptedAuthentication`, and the module warns each time, because the sign-in is sent unencrypted:

```powershell
Connect-PagerArchive -Uri http://192.168.1.20:8095 -AllowUnencryptedAuthentication
```

## Templates

```powershell
Get-PagerArchiveTemplate
New-PagerArchiveTemplate -Name 'Example Rescue' -Agency 'Example Rescue Service' -Colour '#1565c0'
Set-PagerArchiveTemplate -Name 'Example Rescue' -AgencyProfile example-rescue
Rename-PagerArchiveTemplate -Name 'Example Rescue' -NewName 'Example Rescue North'
Remove-PagerArchiveTemplate -Name 'Example Rescue'
```

A template that aliases still use can't be deleted; the error says how many use it. A new agency profile re-parses the template's pages. A renamed template keeps its aliases; a name another template has is refused (`template_exists`).

**Icons** (R65). `-Path` (wildcards, matching one file) or `-LiteralPath` (alias `-PSPath`) sets a template's icon from a file, and `-Uri` from a web address. The module downloads the address on your computer, at most 100 KB and within 30 seconds, sending no credentials, and uploads the file: PagerArchive's server never fetches anything from another site. `-RemoveIcon` removes it. `HasIcon` says whether a template has one.

```powershell
Set-PagerArchiveTemplate -Name 'Example Rescue' -Path ./rescue.png
Set-PagerArchiveTemplate -Name 'Example Rescue' -Uri 'https://example.test/rescue.png'
New-PagerArchiveTemplate -Name 'Example Water' -Agency 'Example Water Service' -Path ./water.ico
Set-PagerArchiveTemplate -Name 'Example Rescue' -RemoveIcon
Get-PagerArchiveTemplate | Where-Object -Property HasIcon -EQ $false
```

PagerArchive accepts PNG, ICO, GIF, JPEG and WebP files of at most 100 KB, judged by their contents, and stores each as a 64×64 PNG. A refused icon is an error with the API's code, such as `icon_svg`, `icon_type` or `icon_too_large`. `New-PagerArchiveTemplate` reads or downloads the icon before adding the template, so a missing file adds nothing; if PagerArchive then refuses the icon, the template stays without one.

## Aliases

```powershell
Get-PagerArchiveAlias -Name 'Station*'
New-PagerArchiveAlias -Pattern 0100005 -Name 'Station 5' -Template 'Example Fire'
Set-PagerArchiveAlias -Pattern 0100005 -Template 'Example Rescue'
Rename-PagerArchiveAlias -Pattern 0100005 -NewName 'Station Five'
Remove-PagerArchiveAlias -Pattern 0100005
```

`-WhatIf` and `-Confirm` work as for built-in commands. Removing asks for confirmation.

### Choosing records

A template or alias piped from `Get-PagerArchiveTemplate` or `Get-PagerArchiveAlias` (or any object with an `Id`) is chosen by its **Id**: the `Remove-`, `Set-` and `Rename-` commands act on exactly that record. This matters when two templates' names differ only in case, or when several aliases share a name:

```powershell
Get-PagerArchiveTemplate -Name 'example fire' | Where-Object -Property Id -EQ 49 | Remove-PagerArchiveTemplate
Get-PagerArchiveAlias -Name 'Brigade' | Where-Object -Property Pattern -EQ '0300002' | Rename-PagerArchiveAlias -NewName 'Brigade South'
Set-PagerArchiveTemplate -Id 49 -Colour '#1565c0'
```

A name typed, or piped as text, is matched as before: case-insensitively and with wildcards, so `Remove-PagerArchiveTemplate -Name 'example fire'` matches both `Example Fire` and `example fire`. Confirmations and `-WhatIf` name each record with its Id (`example fire (Id 49)`, `0300002 (Brigade, Id 4)`), so you can see which one is meant. A CSV from `Export-Csv` keeps the `Id` column, so its rows piped to these commands are chosen by Id too.

### Importing

The import takes rows from the pipeline, usually from `Import-Csv`, and applies them as **one batch**:

```powershell
Import-Csv aliases.csv | Import-PagerArchiveAlias -AgencyProfile @{ 'Example Fire' = 'example-fire' }
```

- The CSV's `address`, `alias`, `agency`, `color` and `ignore` columns bind by name. Other columns (an export's `id` or `icon`, a plugin's settings) are never read.
- New capcodes are added, with one template created per new agency and the profile `-AgencyProfile` maps it to. The mapping can live in a JSON file the deployment keeps: `-AgencyProfile (Get-Content profiles.json -Raw | ConvertFrom-Json -AsHashtable)`. An unknown profile name refuses the whole import.
- Existing aliases aren't changed: each difference comes out as a `Different` result. `-Update` applies them (`Updated`), and asks first.
- Rows for one capcode that are identical are squashed into the first (`Squashed`). Rows that **differ** stop the import, changing nothing, and each is reported as an error. Fix the file, or leave rows out in the pipeline, and run it again:

  ```powershell
  Import-Csv aliases.csv | Where-Object -Property id -NE '10' | Import-PagerArchiveAlias
  ```

- `-WhatIf` is the dry run: nothing changes, not even new capcodes, and every row's result still comes out, with `Applied` false, so you can look at them first:

  ```powershell
  Import-Csv aliases.csv | Import-PagerArchiveAlias -WhatIf | Where-Object -Property Result -EQ 'Different'
  ```

### Exporting

```powershell
Get-PagerArchiveAlias | Export-Csv aliases.csv
```

writes the import format: importing that file reports every row `Unchanged`.

## Messages

```powershell
Get-PagerArchiveMessage -Capcode 0100001 -Priority Emergency
Get-PagerArchiveAlias -Name 'Station*' | Get-PagerArchiveMessage -After (Get-Date).AddDays(-7)
Get-PagerArchiveMessage -Text 'SHED FIRE' -First 20 | Export-Csv fires.csv
Get-PagerArchiveMessage -Unaliased -After (Get-Date).AddDays(-30) | Group-Object -Property Capcode
Get-PagerArchiveMessage -Capcode 0100001 -Skip 20 -First 20
Get-PagerArchiveMessage -First 1 | Format-List
```

Messages come out newest first, with everything the detail view shows: the full text, priority, sent time, capcode, alias, agency, group, job number, category, parsed fields, location, flags and each receiver's time. Filters combine; a filter with several values matches any of them; `-Alias`, `-Agency` and `-Group` accept wildcards. Pages to ignored aliases are included only with `-IncludeIgnored`.

- **Paging:** messages are fetched a page at a time, only as the pipeline asks for more, so `-First 10` or `Select-Object -First 10` makes one request and stops. `-Skip` leaves out the newest matches first. There's no `-IncludeTotalCount`: count with `Measure-Object` or `@(...).Count`, which fetches every match.
- **Display:** a table (sent time, priority, alias or capcode, text) by default. `Format-List` shows what the detail view shows, including each receiver's time.
- **Times:** tables and lists show times in your time zone. The objects' `SentAt` and `ReceivedAt` are the `[datetimeoffset]` values the server sent (UTC), so `Export-Csv` and comparisons don't depend on where you are.

### Watching

```powershell
Watch-PaMessage -Capcode 0100001
Watch-PaMessage -Priority Emergency | ForEach-Object { "$($_.Alias): $($_.Text)" }
Get-PagerArchiveAlias -Name 'Station*' | Watch-PagerArchiveMessage
Watch-PaMessage -JobNumber EX0000041 | Export-Csv job.csv -Append
```

`Watch-PagerArchiveMessage` (`Watch-PaMessage`) outputs each new message the moment it arrives, as the same objects `Get-PagerArchiveMessage` returns, until you press Ctrl+C. It takes the same filters, except `-After`, `-Before`, `-First` and `-Skip`.

- **A lost connection** gives one warning, then it keeps retrying (after 1, 2, 4, 8, 16, then every 30 seconds; `-Verbose` shows each try). When it's back, a second warning says how long it was down and how many messages follow ("Reconnected after 7 s; 3 messages that arrived meanwhile follow."); then it outputs the matching messages that arrived meanwhile, oldest first, and carries on. Each message comes out once.
- **Signing in** is renewed as it goes, so a watch can run for days. It stops with an error, without retrying, if your account is deactivated (`deactivated`), you're no longer an admin (`powershell_admins_only`), or your sign-in has ended (`sign_in_ended`, for example revoked in Authentik; at the latest when the current 5-minute access token expires).
- **Receive times:** each message has the receive times known when it arrived, usually the first receiver's. `Get-PagerArchiveMessage` shows later copies.
- **Proxies:** like every other command, the watch uses the system's proxy settings: `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` if set, otherwise Windows' or macOS's own.
- It's a convenience, not an alerting system: don't rely on it in place of an issued pager.

## Backups

```powershell
Get-PagerArchiveBackup
Start-PagerArchiveBackup -Wait
Start-PagerArchiveBackup -Wait -Timeout (New-TimeSpan -Hours 2) -ErrorAction Stop
```

`Get-PagerArchiveBackup` (`Get-PaBackup`) says how the server's backups are going, as **Administration → Backups** does: `State` is `ok`, `waiting` (none yet on a new install), `failed` (`Error` says why), `overdue` (no good backup for 36 hours) or `off` (no backup service). `Run` is what the backup service is doing now: `idle`, `queued` (asked for: it starts within a minute), `running`, or `off` (backups can't be asked for on this server). It also has the last good backup's time, night, size and how long its dumps and check took.

`Start-PagerArchiveBackup` (`Start-PaBackup`) backs up now, as the browser's **Back up now** does: the same backup and check as the nightly one, kept with the nightly backups and replacing any made earlier that day. Use it before a change you might want to undo, such as an upgrade. It returns once the server has the request; `-Wait` waits until the backup and its check are done, with progress, and writes an error if it failed (`backup_failed`) or isn't done within `-Timeout` (default 30 minutes; `backup_timeout`, and the backup carries on). `-PassThru` outputs the state, and `-WhatIf` asks for nothing. One at a time: while a backup is asked for or running, from here or the browser, another is refused (`backup_queued`, `backup_running`), with since when.

## Errors

A refusal from PagerArchive is an error whose `ErrorId` is the API's code (e.g. `duplicate_alias`, `template_in_use`, `powershell_admins_only`) and whose message is the API's. A failure on one item doesn't stop the rest of the pipeline; not being signed in, or the server being unreachable, does.

## What the server needs

Nothing to set: the PowerShell client (`pagerarchive-powershell`) is part of the Authentik blueprint, and the API finds it from `AUTHENTIK_URL` (`POWERSHELL_CLIENT_ID` changes its ID). `GET /auth/powershell` tells the module where to sign in. A remote admin needs to reach both the app's URL and Authentik's.
