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
        PSUseCorrectCasing               = @{
            Enable = $true
        }
    }
}
