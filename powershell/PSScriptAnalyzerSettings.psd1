@{
    # docs/powershell-guidelines.md: every default rule, plus these, with
    # zero warnings allowed.
    Severity            = @('Error', 'Warning', 'Information')
    IncludeDefaultRules = $true
    ExcludeRules        = @()
    Rules               = @{
        PSProvideCommentHelp             = @{
            Enable                  = $true
            ExportedOnly            = $true
            BlockComment            = $true
            VSCodeSnippetCorrection = $false
            Placement               = 'begin'
        }
        PSUseCompatibleSyntax            = @{
            Enable         = $true
            TargetVersions = @('7.0')
        }
        PSAvoidUsingPositionalParameters = @{
            Enable           = $true
            CommandAllowList = @()
        }
        PSAvoidUsingCmdletAliases        = @{
            AllowList = @()
        }
        PSPlaceOpenBrace                 = @{
            Enable             = $true
            OnSameLine         = $true
            NewLineAfter       = $true
            IgnoreOneLineBlock = $true
        }
        PSPlaceCloseBrace                = @{
            Enable             = $true
            NewLineAfter       = $false
            IgnoreOneLineBlock = $true
            NoEmptyLineBefore  = $false
        }
        PSUseConsistentIndentation       = @{
            Enable              = $true
            IndentationSize     = 4
            PipelineIndentation = 'IncreaseIndentationForFirstPipeline'
            Kind                = 'space'
        }
        PSUseConsistentWhitespace        = @{
            Enable                                  = $true
            CheckInnerBrace                         = $true
            CheckOpenBrace                          = $true
            CheckOpenParen                          = $true
            CheckOperator                           = $false
            CheckPipe                               = $true
            CheckPipeForRedundantWhitespace         = $false
            CheckSeparator                          = $true
            CheckParameter                          = $false
            IgnoreAssignmentOperatorInsideHashTable = $true
        }
        PSAlignAssignmentStatement       = @{
            Enable         = $true
            CheckHashtable = $true
        }
        # Keywords and operators. Command and parameter names are checked by
        # Meta.Tests.ps1 instead: this rule's command check reads a provider
        # cmdlet's dynamic parameters from another thread, which sometimes
        # throws a NullReferenceException in PSScriptAnalyzer 1.25.0
        # (ADR 0012).
        PSUseCorrectCasing               = @{
            Enable        = $true
            CheckCommands = $false
            CheckKeyword  = $true
            CheckOperator = $true
        }
    }
}
