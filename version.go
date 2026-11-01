// Package pagerarchive holds what belongs to the repository as a whole: the
// release's version, read from the VERSION file at the root (SPEC.md section
// 12.1, ADR 0016). The server and the client report it.
package pagerarchive

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is the release's version, such as "0.12.1".
var Version = strings.TrimSpace(versionFile)
