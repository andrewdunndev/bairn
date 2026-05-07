package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
	"gitlab.com/dunn.dev/bairn/internal/sync"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "0.1.0"

const usage = `usage: bairn <subcommand> [flags]

subcommands:
  login    authenticate against Famly with email and password
  fetch    pull new photos and videos to disk (and Immich, if configured)
  status   summarise the most recent fetch
  drift    check the vendor surface for schema drift

run "bairn <sub> -h" for subcommand flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, cancel := signalContext()
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	logger := newLogger(cfg.LogFormat)
	slog.SetDefault(logger)

	switch os.Args[1] {
	case "login":
		os.Exit(runLogin(ctx, cfg, logger, os.Args[2:]))
	case "fetch":
		os.Exit(runFetch(ctx, cfg, logger, os.Args[2:]))
	case "status":
		os.Exit(runStatus(ctx, cfg, logger, os.Args[2:]))
	case "drift":
		os.Exit(runDrift(ctx, cfg, logger, os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func newLogger(format string) *slog.Logger {
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	default:
		return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
}

// runFetch implements the fetch subcommand.
func runFetch(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	maxPages := fs.Int("max-pages", 3, "stop after this many feed pages (0 = unlimited)")
	dryRun := fs.Bool("dry-run", false, "enumerate without saving or uploading")
	feedAll := fs.Bool("feed-all", true, "include every image and video on the feed")
	feedTagged := fs.Bool("feed-tagged", false, "include only images tagged with one of our children")
	feedLiked := fs.Bool("feed-liked", false, "include only images liked by a household login")
	saveDir := fs.String("save-dir", cfg.SaveDir, "root directory for saved photos and videos")
	noImmich := fs.Bool("no-immich", false, "save to disk only; skip Immich even when configured")
	filenamePat := fs.String("filename-pattern", "", "filename template (default: feed-{{.Source}}-%Y-%m-%d_%H-%M-%S-{{.ID}}.{{.Ext}})")
	dirPat := fs.String("dir-pattern", "", "directory template under save-dir (default: %Y-%m-%d)")
	includeSystem := fs.Bool("include-system-posts", false, "include automated Famly posts (check-ins, sign-outs); off by default to keep templated text out of photo captions")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := cfg.Validate("fetch"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	disk, err := sink.NewDisk(*saveDir, *filenamePat, *dirPat)
	if err != nil {
		fmt.Fprintln(os.Stderr, "save-dir:", err)
		return 2
	}

	var immichSink *sink.Immich
	if !*noImmich && cfg.ImmichBaseURL != "" && cfg.ImmichAPIKey != "" {
		ic := immich.New(cfg.ImmichBaseURL, cfg.ImmichAPIKey, immich.WithLogger(logger))
		immichSink = sink.NewImmich(ic)
	}

	tokenSrc := buildTokenSource(cfg)
	fc := famly.New(tokenSrc, famlyOpts(cfg)...)
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	defer st.Close()

	logins := map[string]struct{}{}
	children := map[string]struct{}{}
	if *feedTagged || *feedLiked {
		me, err := fc.Me(ctx)
		if err != nil {
			logger.Error("fetch /me", "err", err)
			return 1
		}
		logins[me.LoginID] = struct{}{}
		for _, r := range me.Roles2 {
			children[r.TargetID] = struct{}{}
			rels, err := fc.Relations(ctx, r.TargetID)
			if err != nil {
				logger.Warn("fetch /relations", "child", r.TargetID, "err", err)
				continue
			}
			for _, rel := range rels {
				if rel.LoginID != "" {
					logins[rel.LoginID] = struct{}{}
				}
			}
		}
	}

	res, err := sync.Run(ctx, sync.Deps{
		Famly: fc, Disk: disk, Immich: immichSink, State: st, Logger: logger,
	}, sync.Options{
		MaxPages:           *maxPages,
		DryRun:             *dryRun,
		Sources:            sync.Sources{FeedAll: *feedAll, FeedTagged: *feedTagged, FeedLiked: *feedLiked},
		HouseholdLogins:    logins,
		HouseholdChildren:  children,
		Software:           "bairn " + Version,
		IncludeSystemPosts: *includeSystem,
	})
	if err != nil {
		logger.Error("fetch", "err", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	return 0
}

// runStatus implements the status subcommand.
func runStatus(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	defer st.Close()
	sm, err := st.Stats(ctx)
	if err != nil {
		logger.Error("stats", "err", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sm)
	return 0
}

// runDrift shells out to the discovery probe today; v0.2.0+ may
// move it to a native Go subcommand using the typed clients.
func runDrift(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	// Drift is implemented as a native subcommand in v0.2.0+; for
	// now, run discovery/probe/shape.py manually.
	logger.Warn("drift",
		"msg", "native drift not yet implemented; run discovery/probe/shape.py manually for now")
	return 0
}

func buildTokenSource(cfg *config.Config) famly.TokenSource {
	// Prefer credentials when present for self-healing cron.
	// Otherwise fall back to the static token.
	if cfg.FamlyEmail != "" && cfg.FamlyPassword != "" {
		return famly.NewRefreshingTokenFromCredentials(cfg.FamlyBaseURL, cfg.FamlyEmail, cfg.FamlyPassword, cfg.FamlyDeviceID)
	}
	return famly.NewStaticToken(cfg.FamlyAccessToken)
}

func famlyOpts(cfg *config.Config) []famly.Option {
	var opts []famly.Option
	if cfg.FamlyBaseURL != "" {
		opts = append(opts, famly.WithBaseURL(cfg.FamlyBaseURL))
	}
	return opts
}
