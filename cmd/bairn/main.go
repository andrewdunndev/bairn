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
	"gitlab.com/dunn.dev/bairn/internal/drift"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
	"gitlab.com/dunn.dev/bairn/internal/sync"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "0.4.5"

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
	source := fs.String("source", "all", "feed filter: all (every image and video), tagged (only images tagged with one of our children), or liked (only images liked by a household login)")
	saveDir := fs.String("save-dir", cfg.SaveDir, "root directory for saved photos and videos")
	noImmich := fs.Bool("no-immich", false, "save to disk only; skip Immich even when configured")
	filenamePat := fs.String("filename-pattern", "", "filename template (default: feed-{{.Source}}-%Y-%m-%d_%H-%M-%S-{{.ID}}.{{.Ext}})")
	dirPat := fs.String("dir-pattern", "", "directory template under save-dir (default: %Y-%m-%d)")
	includeSystem := fs.Bool("include-system-posts", false, "include automated Famly posts (check-ins, sign-outs); off by default to keep templated text out of photo captions")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	src := sync.Source(*source)
	if err := src.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	if src == sync.SourceTagged || src == sync.SourceLiked {
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
		// Trap A: a token that mints fine but resolves to no
		// children/logins makes shouldDownloadImage return false for
		// every image; the run would walk pages and save nothing
		// silently. Fail loudly with a hint at the most likely cause.
		if src == sync.SourceTagged && len(children) == 0 {
			fmt.Fprintln(os.Stderr, "fetch: --source=tagged but no household children visible. Verify the token belongs to an enrolled caregiver (run \"bairn login\" to see Roles2).")
			return 2
		}
		if src == sync.SourceLiked && len(logins) == 0 {
			fmt.Fprintln(os.Stderr, "fetch: --source=liked but no household logins resolved. Verify the token's /me response includes a loginId and at least one Relation.")
			return 2
		}
	}

	res, err := sync.Run(ctx, sync.Deps{
		Famly: fc, Disk: disk, Immich: immichSink, State: st, Logger: logger,
	}, sync.Options{
		MaxPages:           *maxPages,
		DryRun:             *dryRun,
		Source:             src,
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

// runDrift hits the endpoints in a TOML manifest, computes
// JSON-key-only shape signatures, optionally diffs against a prior
// baseline, and writes fresh signatures to an output directory.
//
// Exit codes: 0 = no drift (or no diff requested), 1 = drift found,
// 2 = configuration or transport error. The non-zero-on-drift
// behaviour lets the catalog's claude-drift-triage component fire on
// real changes only.
func runDrift(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "discovery/probe/manifest.toml", "TOML manifest path")
	outDir := fs.String("out-dir", "discovery/baselines/current", "directory for written shape signatures")
	diffDir := fs.String("diff", "", "prior baseline directory to diff against (optional)")
	anonymize := fs.Bool("anonymize", false, "replace array length markers <n=N> with <n=*> so household-side cardinality (e.g. number of relations) does not leak into committed baselines")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := cfg.Validate("drift"); err != nil {
		logger.Error("drift", "phase", "config", "err", err)
		return 2
	}

	m, err := drift.LoadManifest(*manifestPath)
	if err != nil {
		logger.Error("drift", "phase", "manifest", "err", err)
		return 2
	}
	logger.Info("drift",
		"manifest", *manifestPath,
		"endpoints", len(m.Endpoints),
		"out_dir", *outDir,
		"diff", *diffDir,
	)

	// Token resolution by manifest. Famly's auth_env is FAMLY_ACCESS_TOKEN;
	// when that's set, prefer credentials (FAMLY_EMAIL+FAMLY_PASSWORD)
	// over the short-lived static token for a self-healing tag pipeline.
	// Other vendors (Immich's IMMICH_API_KEY, etc.) flow through Probe's
	// env-fallback unchanged: leaving Token empty makes Probe read
	// os.Getenv(m.AuthEnv).
	var token string
	if m.AuthEnv == "FAMLY_ACCESS_TOKEN" {
		tokenSrc := buildTokenSource(cfg)
		token, err = tokenSrc.Token(ctx)
		if err != nil {
			logger.Error("drift", "phase", "token", "err", err)
			return 2
		}
		if token == "" {
			logger.Error("drift", "phase", "token", "err", "resolved Famly token is empty; check FAMLY_EMAIL/FAMLY_PASSWORD or FAMLY_ACCESS_TOKEN are visible to the pipeline (protected vars need a protected ref)")
			return 2
		}
	}

	opts := drift.ProbeOptions{
		Logger:  logger,
		Token:   token,
		Shape:   drift.ShapeOpts{AnonymizeCounts: *anonymize},
		Schemas: driftSchemas,
	}
	// Trap B: a --diff dir that's missing or empty silently produces
	// "no drift found" which masquerades as a healthy gate. Count
	// the comparisons that actually had a prior signature so we can
	// fail loudly when the gate is a passthrough.
	comparedCount := 0
	if *diffDir != "" {
		opts.Compare = func(id string) (any, bool) {
			sig, err := drift.ReadSignature(*diffDir, id)
			if err != nil {
				return nil, false
			}
			comparedCount++
			return sig, true
		}
	}

	results, err := drift.Probe(ctx, m, opts)
	if err != nil {
		logger.Error("drift", "phase", "probe", "err", err)
		return 2
	}

	driftCount := 0
	writeFails := 0
	for _, r := range results {
		switch {
		case r.Error != "":
			fmt.Printf("  %s: ERROR %s\n", r.ID, r.Error)
		case r.NotJSON:
			fmt.Printf("  %s: HTTP %d, %dB, not JSON\n", r.ID, r.Status, r.BodySize)
		default:
			if err := drift.WriteSignature(*outDir, r.ID, r.Signature); err != nil {
				logger.Error("drift", "phase", "write", "id", r.ID, "err", err)
				writeFails++
			}
			switch {
			case len(r.Drift) > 0:
				driftCount++
				fmt.Printf("  %s: HTTP %d, %dB, DRIFT (%d changes)\n", r.ID, r.Status, r.BodySize, len(r.Drift))
				for _, d := range r.Drift {
					fmt.Printf("    %s\n", d)
				}
			case *diffDir != "":
				fmt.Printf("  %s: HTTP %d, %dB, ok\n", r.ID, r.Status, r.BodySize)
			default:
				fmt.Printf("  %s: HTTP %d, %dB\n", r.ID, r.Status, r.BodySize)
			}
		}
	}

	// Trap B (cont.): if --diff was set and zero comparisons
	// resolved, the gate compared nothing. Fail with exit 2 so a
	// passthrough doesn't pass for a working gate.
	if *diffDir != "" && comparedCount == 0 && len(results) > 0 {
		logger.Error("drift",
			"phase", "compare",
			"err", "no prior signatures found",
			"diff_dir", *diffDir,
			"endpoints", len(results),
			"hint", "seed the baseline first: bairn drift --anonymize --out-dir "+*diffDir+" (then commit). Until seeded, --diff is a no-op.",
		)
		return 2
	}

	if writeFails > 0 {
		return 2
	}
	if driftCount > 0 {
		return 1
	}
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
