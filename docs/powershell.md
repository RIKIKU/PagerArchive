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
Remove-PagerArchiveTemplate -Name 'Example Rescue'
```

A template that aliases still use can't be deleted; the error says how many use it. A new agency profile re-parses the template's pages.

## Aliases

```powershell
Get-PagerArchiveAlias -Name 'Station*'
New-PagerArchiveAlias -Pattern 0100005 -Name 'Station 5' -Template 'Example Fire'
Set-PagerArchiveAlias -Pattern 0100005 -Template 'Example Rescue'
Rename-PagerArchiveAlias -Pattern 0100005 -NewName 'Station Five'
Remove-PagerArchiveAlias -Pattern 0100005
```

`-WhatIf` and `-Confirm` work as for built-in commands. Removing asks for confirmation.

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
```

Messages come out newest first, with everything the detail view shows: the full text, priority, sent time, capcode, alias, agency, group, job number, category, parsed fields, location, flags and each receiver's time. Filters combine; a filter with several values matches any of them; `-Alias`, `-Agency` and `-Group` accept wildcards. Pages to ignored aliases are included only with `-IncludeIgnored`. Messages are fetched a page at a time, only as the pipeline asks for more, so `-First 10` or `Select-Object -First 10` stops early.

## Errors

A refusal from PagerArchive is an error whose `ErrorId` is the API's code (e.g. `duplicate_alias`, `template_in_use`, `powershell_admins_only`) and whose message is the API's. A failure on one item doesn't stop the rest of the pipeline; not being signed in, or the server being unreachable, does.

## What the server needs

Nothing to set: the PowerShell client (`pagerarchive-powershell`) is part of the Authentik blueprint, and the API finds it from `AUTHENTIK_URL` (`POWERSHELL_CLIENT_ID` changes its ID). `GET /auth/powershell` tells the module where to sign in. A remote admin needs to reach both the app's URL and Authentik's.
