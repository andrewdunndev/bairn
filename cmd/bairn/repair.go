package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// repairSummary is what `repair missing` prints.
type repairSummary struct {
	Considered int `json:"considered"`
	Marked     int `json:"marked"`
	Present    int `json:"present"`
	Errors     int `json:"errors"`
}

// missingFileError is the lastError a repaired entry carries.
const missingFileError = "saved file missing"

// runRepair implements the repair subcommand. It is offline: it reads
// the state file and stats the saved files, and nothing else.
func runRepair(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	if len(args) == 0 || args[0] != "missing" {
		fmt.Fprintln(os.Stderr, "usage: bairn repair missing [--dry-run]")
		return 2
	}
	fl := flag.NewFlagSet("repair missing", flag.ExitOnError)
	dryRun := fl.Bool("dry-run", false, "count what would be marked without writing the state file")
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	// Open takes the exclusive state lock, so this cannot run beside a fetch.
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	sum := repairMissing(st, *dryRun, logger)
	if err := st.Close(); err != nil {
		logger.Error("repair: write state", "err", err)
		sum.Errors++
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sum)
	if sum.Errors > 0 {
		return 1
	}
	return 0
}

// repairMissing marks every saved entry whose file is missing (the
// definition status --missing uses) as failed, in one store update, so
// `fetch --retry-failed` downloads it again. A stat failure other than
// absence is an error and leaves the entry alone.
func repairMissing(st *state.Store, dryRun bool, logger *slog.Logger) repairSummary {
	var sum repairSummary
	var ids []string
	for _, f := range st.SavedFiles() {
		sum.Considered++
		_, err := statSaved(f.SavedPath)
		switch {
		case err == nil:
			sum.Present++
		case errors.Is(err, errNoSavedFile):
			ids = append(ids, f.ID)
		default:
			// Same as status: any failed stat counts as missing there, but
			// repair only acts on a file known to be absent.
			logger.Error("repair: stat", "id", f.ID, "err", err)
			sum.Errors++
		}
	}
	if dryRun {
		sum.Marked = len(ids)
		return sum
	}
	sum.Marked = st.Requeue(ids, missingFileError)
	return sum
}
