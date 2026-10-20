# PowerShell module guidelines

Owner, 2026-10-20. These rules apply to the **PagerArchive** PowerShell module (SPEC.md R59–R63). They are based on Microsoft's [Cmdlet Development Guidelines](https://learn.microsoft.com/powershell/scripting/developer/cmdlet/cmdlet-development-guidelines) and its required, strongly encouraged and advisory pages, adapted for a script module. Where this file and Microsoft's pages disagree, this file wins.

**The goal:** the module puts the user experience first. An experienced PowerShell user should find it predictable without reading the docs: commands named as they'd guess, objects rather than text, the pipeline working between commands, and `-WhatIf`, `-Confirm` and `Get-Help` behaving as they do for built-in cmdlets.

Each rule says how it's checked: **[analyzer]** is PSScriptAnalyzer in CI, **[pester]** is a Pester test in CI, and **[review]** is checked by reading the code. Prefer a tool check: if a rule can be checked by a tool, add the check.

## 1. Platform and form

- **PowerShell 7 or later only** (owner). The manifest sets `CompatiblePSEditions = 'Core'` and `PowerShellVersion = '7.6'`, because 7.4 and 7.5 lose support on 2026-11-10 (owner, 2026-10-21). The minimum is raised in a normal change when a release loses support, never by a test that fails on a date. CI tests on the minimum and on the newest, on Windows and Linux. **[pester]**
- **A script module of advanced functions, not compiled C#** (owner). Use compiled code only where it has a real advantage, which needs the owner's agreement. Microsoft's code guidelines map onto advanced functions as follows:

  | Microsoft's guideline | In this module |
  |---|---|
  | Derive from `PSCmdlet`, Cmdlet attribute (RC01, RC02) | `[CmdletBinding()]` on every exported function |
  | Override an input processing method (RC03) | `begin`, `process` and `end` blocks. Pipeline input is handled in `process` |
  | `OutputType` attribute (RC04) | `[OutputType()]` on every exported function |
  | Handle errors robustly (RC06) | `$PSCmdlet.WriteError()` and `$PSCmdlet.ThrowTerminatingError()` (section 7) |
  | Deploy as a module (RC07) | A manifest (`.psd1`) and a root module (`.psm1`) |
  | Help file (SD05) | Comment-based help (section 8) |
  | Standard members, display (SC01) | `Types.ps1xml` and `Format.ps1xml` (section 5) |

## 2. Names

- **Approved verbs only**, as listed by `Get-Verb`, and never a synonym Microsoft lists to avoid: `Remove`, not `Delete`. **[analyzer]** `PSUseApprovedVerbs`
- Use each verb for what it means. `New` creates. `Set` changes an existing item. `Add` puts something into a container. `Remove` deletes. `Rename` changes a name. `Import` creates items from data. `Approve`, `Deny` and `Submit` are for requests. **[review]**
- **Nouns are singular PascalCase and start with `PagerArchive`** (owner), e.g. `Get-PagerArchiveAlias`, `New-PagerArchiveTemplate`. The sign-in commands use the product as the noun: `Connect-PagerArchive`, `Disconnect-PagerArchive`. **[analyzer]** `PSUseSingularNouns`, **[pester]** prefix
- **Every exported command has a short alias** made by replacing `PagerArchive` with `Pa` (owner), e.g. `Get-PaAlias`, `Connect-Pa`. **[pester]**
- **Parameter names** are standard where one exists (`Name`, `Path`, `LiteralPath`, `InputObject`, `Force`, `PassThru`, `Uri`), singular, PascalCase, and never a common parameter's name (RD03). Other accepted spellings go in `[Alias()]`, never in a second parameter. **[analyzer]** `PSReservedParams`, **[review]**

## 3. Parameters and the pipeline

- **Every parameter set has at least one parameter that takes pipeline input** (SC02). At most one parameter per set has `ValueFromPipeline`. Parameters that match an object's properties use `ValueFromPipelineByPropertyName`. **[pester]**
- **Output objects and parameters line up.** A property that identifies or describes an item has the same name as the parameter that takes it, so `Get-PagerArchiveAlias -Name 'Station*' | Set-PagerArchiveAlias -Template 'Example Rescue'` works. Data from elsewhere binds through `[Alias()]`: the import CSV's column names (`address`, `alias`, `color`) are aliases of the import's parameters. **[pester]**
- **Strong types.** Use `[uri]` for URLs, an enum or `[ValidateSet()]` for a fixed list, `[switch]` for on/off (never `[bool]`), and `[Nullable[bool]]` when "leave unchanged" is a third choice. Use the same type for the same parameter everywhere. Use plain `[string]` only for free text and names. **[review]**
- **Arrays where users act on several items.** `-Name` takes `[string[]]`. **[review]**
- **Wildcards on `-Name`** wherever it selects existing items, and the parameter's help says so (section 8). Matching is case-insensitive and case-preserving (SC04). **[pester]**
- **Files** (from M9: icons) use `-Path`, which allows wildcards and has a `PSPath` alias, and `-LiteralPath`. Both are resolved through PowerShell's provider paths, so `~` and PowerShell drives work. **[review]**
- **The default parameter set** is the one people use most at the prompt. **[review]**
- **Commands that can return many items** (e.g. `Get-PagerArchiveMessage`) declare `SupportsPaging` (`-First`, `-Skip`). They fetch pages from the API only as the pipeline asks for more, so `-First 10` or `Select-Object -First 10` stops further requests. **[pester]**
- **Time ranges** use `-After` and `-Before`, typed `[datetimeoffset]`, so `(Get-Date).AddDays(-7)` just works. **[review]**

## 4. Changes, confirmation and `-Force`

- **Every command that changes data** declares `SupportsShouldProcess` and calls `$PSCmdlet.ShouldProcess()` once per item before changing it (RD04). `-WhatIf` is the dry run, and there's no separate dry-run switch. **Import is the exception:** its rows are one all-or-nothing batch (R27a), so it calls `ShouldProcess` once for the batch, with a summary such as "412 aliases: 380 new, 12 different", and under `-WhatIf` it still outputs every row's result, marked as not applied. **[analyzer]** `PSUseShouldProcessForStateChangingFunctions`, **[pester]**
- **`ConfirmImpact`** is `High` for `Remove-*` and for import with `-Update`, and `Medium` otherwise. **[pester]**
- **`-Force`** only where a command asks for more than `ShouldProcess` (RD05, AD02). It never gets around a rule the API enforces. **[review]**
- **What changes output:** `New-*` and `Import-*` output what they created or did. `Set-*`, `Rename-*` and `Remove-*` output nothing unless `-PassThru` is given (SD03). **[pester]**

## 5. Output

- **Objects, never text.** Each kind has a type name (`PagerArchive.Alias`, `PagerArchive.Template`, `PagerArchive.ImportResult`, …) and `[OutputType()]` names it. Never use `Write-Host`, `Format-*` or strings for data. **[analyzer]** `PSAvoidUsingWriteHost`, `PSUseOutputTypeCorrectly`, **[pester]**
- **Write each object as soon as you have it** (SC03). Don't collect them into an array first. **[review]**
- **Default display:** a `Format.ps1xml` table view with the few columns people scan, plus a `DefaultDisplayPropertySet` in `Types.ps1xml`. Every property is still there for `Select-Object *` and `Export-Csv`. **[pester]**
- **Times** are `[datetimeoffset]` values, so they compare, sort and export correctly in any time zone. **[review]**
- **Document every output type** in `.OUTPUTS`, saying what its properties are for (RD06). **[pester]**

## 6. Feedback

- `Write-Verbose` for each API call (method and path). `Write-Debug` for detail useful in a fault. `Write-Warning` when a result may surprise the user. `Write-Progress` for anything that can take more than a few seconds, such as a large import. Never `[Console]` (SD04). **[review]**
- **Verbose, debug, warning and error text never contains a token, a page's text or an address** (P1). **[pester]** runs the commands with `-Verbose -Debug` and searches the output.

## 7. Errors

- **A refusal from the API becomes an `ErrorRecord`.** Its `ErrorId` is the API's error code (e.g. `last_admin`). Its message is the API's message. Its `TargetObject` is the input item. Its category comes from this table: **[pester]**

  | API response | Category |
  |---|---|
  | 400, 422 | `InvalidArgument` |
  | 401 | `AuthenticationError` |
  | 403 | `PermissionDenied` |
  | 404 | `ObjectNotFound` |
  | 409, already exists | `ResourceExists` |
  | 409, any other conflict | `InvalidOperation` |
  | 429 | `LimitsExceeded` |
  | 5xx, or the server can't be reached | `ConnectionError` |

- **A failure on one item is non-terminating** (`$PSCmdlet.WriteError()`), so the rest of the pipeline carries on (RC06). A failure that stops every item, such as not being signed in or the server being unreachable, is terminating (`$PSCmdlet.ThrowTerminatingError()`). Never `throw` a string. **[review]**
- **Import is the exception, on purpose:** its rows are applied as one batch (R27a), so a conflict means nothing is applied. Each conflicting row is reported as its own non-terminating error, so the user sees them all at once. **[pester]**

## 8. Help

- **Comment-based help** (owner), at the top of each function body, with `.SYNOPSIS`, `.DESCRIPTION`, at least one `.EXAMPLE`, `.INPUTS`, `.OUTPUTS` and `.LINK` to related commands. Examples are real commands a user would type, with the output where it helps. **[analyzer]** `PSProvideCommentHelp`, **[pester]**
- **Parameters are documented by a comment directly above the parameter**, inside `param()` (owner). **Never use `.PARAMETER`**: it silently overrides the parameter's comment. **[pester]** no `.PARAMETER`, and every parameter has help text in `Get-Help -Full`
- **A parameter's comment states its default and whether it accepts wildcards**, because `Get-Help` doesn't show either in its parameter table. **[review]**
- **`about_PagerArchive`** (`en-US/about_PagerArchive.help.txt`) explains signing in, the objects, and common pipelines. **[pester]** exists

## 9. Security

- **Tokens are `[securestring]` from start to finish** (AC06). They're sent with `Invoke-RestMethod -Authentication Bearer -Token` and are never converted to plain text in a variable. They are never written to a file, shown, logged, put in an error, or kept in `$Error` or a transcript. They're saved only through SecretManagement (R61). **[analyzer]** `PSAvoidUsingPlainTextForPassword`, `PSAvoidUsingConvertToSecureStringWithPlainText`, **[pester]**
- **HTTPS only.** Plain HTTP (a LAN test copy) needs an explicit `-AllowUnencryptedAuthentication`, the same name `Invoke-RestMethod` uses, and gives a warning. **[pester]**
- **The module never sends anything except to the PagerArchive server and its Authentik.** Downloading an icon from a URL (M9) is the user's own request and happens on their machine (R65). **[review]**

## 10. Layout and style

- **Layout:**
  - `PagerArchive.psd1`;
  - `PagerArchive.psm1`;
  - `Public/` (one exported function per file, named after it);
  - `Private/`;
  - the format and types files;
  - `en-US/`.

  The manifest lists `FunctionsToExport` and `AliasesToExport` explicitly, never `*`. **[pester]**
- **Code style:** PSScriptAnalyzer's code formatting rules with braces on the same line (`CodeFormattingOTBS`) and 4-space indents. The settings file is in the repo. **[analyzer]**
- **Code is written in full:** full command names, named parameters, no aliases and no positional arguments. Use splatting for long calls. Use `Set-StrictMode -Version Latest` in the module. **[analyzer]** `PSAvoidUsingCmdletAliases`, `PSAvoidUsingPositionalParameters`

## 11. Tests and publishing

- **PSScriptAnalyzer runs in CI with zero warnings allowed.** Pester 5 runs the meta-tests above for every exported command, plus the behaviour checks in SPEC.md (A51–A54) against the real stack. **[pester]**
- **Publishing:** the module is published to the PowerShell Gallery as **PagerArchive** by CI, only from a release whose checks all passed, and with the release's version (owner). The Gallery key is a CI secret the owner sets. Nothing is published from a branch.
