package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// prunedSummary is what `repair pruned` prints.
type prunedSummary struct {
	Considered    int `json:"considered"`
	Unpruned      int `json:"unpruned"`
	Requeued      int `json:"requeued"`
	NotRestorable int `json:"notRestorable"`
	Errors        int `json:"errors"`
}

// prunedRestoreError is the lastError a restored entry carries.
const prunedRestoreError = "restored after prune"

// runRepairPruned implements `repair pruned`. It is offline: it reads
// the state file and stats the media paths, and nothing else.
func runRepairPruned(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fl := flag.NewFlagSet("repair pruned", flag.ExitOnError)
	dryRun := fl.Bool("dry-run", false, "count what would change without writing the state file")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	// Open takes the exclusive state lock, so this cannot run beside a fetch.
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	sum := repairPruned(st, fl.Args(), *dryRun, logger)
	if err := st.Close(); err != nil {
		logger.Error("repair: write state", "err", err)
		sum.Errors++
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sum)
	if sum.NotRestorable > 0 {
		fmt.Fprintf(os.Stderr, "repair pruned: %d entries are no longer on Famly; their copy lives in Immich or the backups\n", sum.NotRestorable)
	}
	if sum.Requeued > 0 && !*dryRun {
		fmt.Fprintln(os.Stderr, "next: bairn fetch --retry-failed")
	}
	if sum.Errors > 0 {
		return 1
	}
	return 0
}

// repairPruned restores the pruned entries (all, or only ids) in one
// store mutation pass: a file already on disk just loses its tombstone;
// an entry Famly no longer serves is left alone; the rest are requeued
// for re-download, keeping the Immich receipt so nothing is uploaded
// twice. An id that is unknown or not pruned counts as an error and
// blocks nothing else. A dry run changes nothing.
func repairPruned(st *state.Store, ids []string, dryRun bool, logger *slog.Logger) prunedSummary {
	var sum prunedSummary
	all := st.PrunedEntries()
	pick := all
	if len(ids) > 0 {
		byID := make(map[string]state.PruneEntry, len(all))
		for _, e := range all {
			byID[e.ID] = e
		}
		pick = nil
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			e, ok := byID[id]
			if !ok {
				logger.Error("repair pruned: unknown or not pruned", "id", id)
				sum.Errors++
				continue
			}
			pick = append(pick, e)
		}
	}
	for _, e := range pick {
		sum.Considered++
		present := false
		if e.SavedPath != "" {
			_, err := os.Lstat(e.SavedPath)
			switch {
			case err == nil:
				present = true
			case errors.Is(err, fs.ErrNotExist):
			default:
				logger.Error("repair pruned: stat", "id", e.ID, "err", err)
				sum.Errors++
				continue
			}
		}
		switch {
		case present:
			sum.Unpruned++
			if !dryRun {
				st.ClearPruned(e.ID)
			}
		case !e.GoneAt.IsZero():
			sum.NotRestorable++
		default:
			sum.Requeued++
			if !dryRun {
				st.RestorePruned(e.ID, prunedRestoreError)
			}
		}
	}
	return sum
}
