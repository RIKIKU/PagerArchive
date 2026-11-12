// Package pagerarchive holds what belongs to the repository as a whole: the
// build's version, read from the VERSION file at the root (SPEC.md section
// 12.1; ADR 0016, ADR 0024). The server and the client report it.
//
// The repository's own VERSION says 0.0.0-dev. CI writes each build's version
// into it before building (scripts/ci/stamp-version.sh): a release's, such as
// 1.7.6, or for any other build the next release's with -dev+<commit>, such
// as 1.7.6-dev+3f2c1ab, so a build of main is never taken for a release.
package pagerarchive

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is the build's version, such as "1.7.6" or "1.7.6-dev+3f2c1ab".
var Version = strings.TrimSpace(versionFile)
