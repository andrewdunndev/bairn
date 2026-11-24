package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// verifyResult values stored on the entry. Verified is the empty string.
const (
	resVerified = ""
	resTrashed  = "trashed"
	resMismatch = "mismatch"
	resAbsent   = "absent"
	resUnverif  = "unverifiable"
)

// verifySummary is what `verify` prints.
type verifySummary struct {
	Verified       int `json:"verified"`
	Trashed        int `json:"trashed"`
	Mismatch       int `json:"mismatch"`
	AbsentInImmich int `json:"absentInImmich"`
	Unverifiable   int `json:"unverifiable"`
	Errors         int `json:"errors"`
}

// runVerify implements `bairn verify [--dry-run]` against the configured sink.
func runVerify(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	return runVerifyOn(ctx, configuredSink(cfg, logger), cfg, logger, args)
}

// runVerifyOn asks the sink's Confirmer, one batch at a time, whether it
// still holds each uploaded entry's bytes, and stamps verifiedAt or
// verifyResult. It never touches a file.
func runVerifyOn(ctx context.Context, s sink.Sink, cfg *config.Config, logger *slog.Logger, args []string) int {
	fl := flag.NewFlagSet("verify", flag.ExitOnError)
	dryRun := fl.Bool("dry-run", false, "make the confirm calls and count the outcomes without writing the state file")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if fl.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: bairn verify [--dry-run]")
		return 2
	}
	cf, ok := confirmerOf("verify", s)
	if !ok {
		return 2
	}
	// Open takes the exclusive state lock, so this cannot run beside a fetch.
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	sum := verifyEntries(ctx, st, cf, *dryRun, time.Now().UTC(), logger)
	if err := st.Close(); err != nil {
		logger.Error("verify: write state", "err", err)
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

// resultFor maps a Confirmer's outcome to the verify result stored on
// the entry.
func resultFor(o sink.Outcome) string {
	switch o {
	case sink.Held:
		return resVerified
	case sink.Trashed:
		return resTrashed
	case sink.Mismatch:
		return resMismatch
	}
	return resAbsent
}

// verifyEntries checks every uploaded, unpruned entry in batches and
// stamps the outcome, flushing the state after each batch. A batch that
// fails stamps nothing, adds its entries to Errors and moves on, except
// that an auth failure, a cancelled context or a failed state write
// stops the run, counting every entry not yet checked. An entry with no
// uploadSha1 or no immichAssetId is unverifiable: no call is made, and
// it is stamped unverifiable so an earlier verification never survives.
func verifyEntries(ctx context.Context, st *state.Store, cf sink.Confirmer, dryRun bool, at time.Time, logger *slog.Logger) verifySummary {
	var sum verifySummary
	var todo []state.VerifyCandidate
	for _, c := range st.VerifyCandidates() {
		if c.UploadSHA1 == "" || c.ImmichAssetID == "" {
			sum.Unverifiable++
			if !dryRun {
				st.StampVerify(c.ID, resUnverif, at)
			}
			continue
		}
		todo = append(todo, c)
	}
	for i := 0; i < len(todo); i += cf.ConfirmBatch() {
		batch := todo[i:min(i+cf.ConfirmBatch(), len(todo))]
		abort, err := verifyBatchOnce(ctx, st, cf, batch, dryRun, at, &sum)
		if err != nil {
			logger.Error("verify: batch failed; nothing stamped for it", "entries", len(batch), "err", err)
			sum.Errors += len(batch)
		}
		if abort {
			sum.Errors += len(todo) - i - len(batch)
			break
		}
	}
	return sum
}

// verifyBatchOnce checks one batch. It stamps only after the whole reply
// is accepted and counts only after the state write, so a failed batch
// leaves no partial result and is counted once, under Errors.
// abort reports an error that no later batch can survive.
func verifyBatchOnce(ctx context.Context, st *state.Store, cf sink.Confirmer, batch []state.VerifyCandidate, dryRun bool, at time.Time, sum *verifySummary) (abort bool, err error) {
	items := make([]sink.ConfirmItem, len(batch))
	for i, c := range batch {
		items[i] = sink.ConfirmItem{ID: c.ID, RemoteID: c.ImmichAssetID, SHA1: c.UploadSHA1}
	}
	res, err := cf.Confirm(ctx, items)
	if err != nil {
		return errors.Is(err, sink.ErrUnauthorized) || ctx.Err() != nil, err
	}
	byID := make(map[string]sink.ConfirmResult, len(res))
	for _, r := range res {
		byID[r.ID] = r
	}
	if len(byID) != len(batch) {
		return false, fmt.Errorf("reply covers %d of %d entries", len(byID), len(batch))
	}
	outcomes := make([]string, len(batch))
	for i, c := range batch {
		r, ok := byID[c.ID]
		if !ok {
			return false, errors.New("reply is missing a requested entry")
		}
		outcomes[i] = resultFor(r.Outcome)
	}
	var tally verifySummary
	for i, c := range batch {
		switch outcomes[i] {
		case resVerified:
			tally.Verified++
		case resTrashed:
			tally.Trashed++
		case resMismatch:
			tally.Mismatch++
		case resAbsent:
			tally.AbsentInImmich++
		}
		if !dryRun {
			st.StampVerify(c.ID, outcomes[i], at)
		}
	}
	if !dryRun {
		if err := st.Flush(); err != nil {
			return true, fmt.Errorf("write state: %w", err)
		}
	}
	sum.Verified += tally.Verified
	sum.Trashed += tally.Trashed
	sum.Mismatch += tally.Mismatch
	sum.AbsentInImmich += tally.AbsentInImmich
	return false, nil
}
