package reparse

import (
	"context"
	"slices"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/longjob"
)

// The re-parse is a long job (R91), with what internal/longjob gives every
// one: State is where it is (R23c), and Progress how far it has got (R23b),
// for /metrics, the admin's status and the progress log lines. Counts only:
// never a capcode pattern, a page or its text.
type (
	State            = longjob.State
	Progress         = longjob.Progress
	ProgressSnapshot = longjob.Snapshot
	Refusal          = longjob.Refusal
)

const (
	Idle    = longjob.Idle
	Running = longjob.Running
	Paused  = longjob.Paused
	Stopped = longjob.Stopped
)

// finished is a run's state in the database once it's done: idle.
const finished = longjob.Finished

func stateOf(dbState string) State { return longjob.StateOf(dbState) }

// logProgress logs "re-parse progress" every w.ProgressEvery (30 s by
// default) until the returned function is called. It runs beside the
// re-parse, so a re-parse that's stuck still says so: done stops moving.
func (w *Worker) logProgress(ctx context.Context) func() {
	return longjob.LogProgress(ctx, w.ProgressEvery, &w.Progress, func(ctx context.Context, args ...any) { w.Log.InfoContext(ctx, "re-parse progress", args...) })
}

// knownReasons are the reasons the code queues a re-parse with (R23a). Only these
// reach a log line, as codes; any tool can queue a request, with any text.
var knownReasons = []string{
	"profiles changed", "retry", "alias added", "alias capcode changed", "alias template changed",
	"alias ignored changed", "alias deleted", "alias import", "template profile changed",
}

// reasonCodes is the requests' reasons as codes, each once and sorted:
// "profiles changed" is profiles_changed, and a reason that isn't one of
// knownReasons is "other".
func reasonCodes(reasons []string) string {
	var out []string
	for _, r := range reasons {
		c := "other"
		if slices.Contains(knownReasons, r) {
			c = strings.ReplaceAll(r, " ", "_")
		}
		out = append(out, c)
	}
	slices.Sort(out)
	return strings.Join(slices.Compact(out), ",")
}
