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

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// verifyBatch is how many entries one bulk-upload-check call covers.
const verifyBatch = immich.MaxBulkCheck

// verifyResult values stored on the entry. Verified is the empty string.
const (
	resVerified = ""
	resTrashed  = "trashed"
	resMismatch = "mismatch"
	resAbsent   = "absent"
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

// bulkChecker is the Immich call verify needs.
type bulkChecker interface {
	BulkUploadCheck(ctx context.Context, items []immich.CheckItem) ([]immich.CheckResult, error)
}

// runVerify implements `bairn verify [--dry-run]`: ask Immich, with one
// bulk-upload-check per batch, whether it still holds each uploaded
// entry's bytes, and stamp immichVerifiedAt or verifyResult. It never
// touches a file.
func runVerify(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fl := flag.NewFlagSet("verify", flag.ExitOnError)
	dryRun := fl.Bool("dry-run", false, "make the Immich calls and count the outcomes without writing the state file")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if fl.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: bairn verify [--dry-run]")
		return 2
	}
	if cfg.ImmichBaseURL == "" || cfg.ImmichAPIKey == "" {
		fmt.Fprintln(os.Stderr, "verify: IMMICH_BASE_URL and IMMICH_API_KEY are required")
		return 2
	}
	// Open takes the exclusive state lock, so this cannot run beside a fetch.
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	ic := immich.New(cfg.ImmichBaseURL, cfg.ImmichAPIKey, immich.WithLogger(logger))
	sum := verifyEntries(ctx, st, ic, *dryRun, time.Now().UTC(), logger)
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

// classify maps Immich's answer for one entry to its verify result. An
// entry is verified only when the checksum maps to the entry's own
// immichAssetId and that asset is not trashed.
func classify(c state.VerifyCandidate, r immich.CheckResult) string {
	switch {
	case !r.Duplicate:
		return resAbsent
	case r.AssetID != c.ImmichAssetID:
		return resMismatch
	case r.IsTrashed:
		return resTrashed
	}
	return resVerified
}

// verifyEntries checks every uploaded, unpruned entry in batches and
// stamps the outcome, flushing the state after each batch. A batch that
// fails stamps nothing, adds its entries to Errors and moves on, except
// that an auth failure, a cancelled context or a failed state write
// stops the run, counting every entry not yet checked. An entry with no
// uploadSha1 or no immichAssetId is unverifiable: no call, no stamp.
func verifyEntries(ctx context.Context, st *state.Store, ic bulkChecker, dryRun bool, at time.Time, logger *slog.Logger) verifySummary {
	var sum verifySummary
	var todo []state.VerifyCandidate
	for _, c := range st.VerifyCandidates() {
		if c.UploadSHA1 == "" || c.ImmichAssetID == "" {
			sum.Unverifiable++
			continue
		}
		todo = append(todo, c)
	}
	for i := 0; i < len(todo); i += verifyBatch {
		batch := todo[i:min(i+verifyBatch, len(todo))]
		abort, err := verifyBatchOnce(ctx, st, ic, batch, dryRun, at, &sum)
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

// verifyBatchOnce checks one batch. It stamps and counts only after the
// whole reply is accepted, so a failed batch leaves no partial result.
// abort reports an error that no later batch can survive.
func verifyBatchOnce(ctx context.Context, st *state.Store, ic bulkChecker, batch []state.VerifyCandidate, dryRun bool, at time.Time, sum *verifySummary) (abort bool, err error) {
	items := make([]immich.CheckItem, len(batch))
	for i, c := range batch {
		items[i] = immich.CheckItem{ID: c.ID, Checksum: c.UploadSHA1}
	}
	res, err := ic.BulkUploadCheck(ctx, items)
	if err != nil {
		return errors.Is(err, immich.ErrUnauthorized) || ctx.Err() != nil, err
	}
	byID := make(map[string]immich.CheckResult, len(res))
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
		outcomes[i] = classify(c, r)
	}
	for i, c := range batch {
		switch outcomes[i] {
		case resVerified:
			sum.Verified++
		case resTrashed:
			sum.Trashed++
		case resMismatch:
			sum.Mismatch++
		case resAbsent:
			sum.AbsentInImmich++
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
	return false, nil
}
