package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// backfillSummary is what `backfill upload-sha1` prints.
type backfillSummary struct {
	Considered      int `json:"considered"`
	Hashed          int `json:"hashed"`
	SkippedModified int `json:"skippedModified"`
	MissingFile     int `json:"missingFile"`
	Errors          int `json:"errors"`
}

// runBackfill implements the backfill subcommand. It is offline: it
// reads the state file and the saved files, and nothing else.
func runBackfill(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	if len(args) == 0 || args[0] != "upload-sha1" {
		fmt.Fprintln(os.Stderr, "usage: bairn backfill upload-sha1 [--dry-run]")
		return 2
	}
	fl := flag.NewFlagSet("backfill upload-sha1", flag.ExitOnError)
	dryRun := fl.Bool("dry-run", false, "count what would be hashed without writing the state file")
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	// Open takes the exclusive state lock, so this cannot run beside a fetch.
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	sum := backfillUploadSHA1(st, *dryRun, logger)
	if err := st.Close(); err != nil {
		logger.Error("backfill: write state", "err", err)
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

// backfillUploadSHA1 fills a blank UploadSHA1 from the saved file. The
// file is the bytes Immich received (Immich.Put hashes the file at
// SavedPath), but only while nothing rewrote it after upload, so an
// entry whose ExifFixedAt or file mtime is after UploadedAt is left
// blank. Stamps are deferred to the caller's Flush or Close.
func backfillUploadSHA1(st *state.Store, dryRun bool, logger *slog.Logger) backfillSummary {
	var sum backfillSummary
	for _, c := range st.WithoutUploadSHA1() {
		sum.Considered++
		if c.ExifFixedAt.After(c.UploadedAt) {
			sum.SkippedModified++
			continue
		}
		if c.SavedPath == "" {
			sum.MissingFile++
			continue
		}
		fi, err := os.Stat(c.SavedPath)
		if errors.Is(err, fs.ErrNotExist) {
			sum.MissingFile++
			continue
		}
		if err != nil || !fi.Mode().IsRegular() {
			logger.Error("backfill: stat", "id", c.ID, "err", err)
			sum.Errors++
			continue
		}
		if fi.ModTime().After(c.UploadedAt) {
			sum.SkippedModified++
			continue
		}
		h, err := hashFile(c.SavedPath)
		if err != nil {
			logger.Error("backfill: hash", "id", c.ID, "err", err)
			sum.Errors++
			continue
		}
		sum.Hashed++
		if !dryRun {
			st.StampUploadSHA1(c.ID, h)
		}
	}
	return sum
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
