package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/contract"
	"gitlab.com/dunn.dev/bairn/internal/safehttp"
)

// runSmoke implements the smoke subcommand: a tag-time gate that
// verifies a sink's wire contract against a live server. Today only
// the immich target is supported; the same shape applies to any
// future sink whose contract isn't fully captured by a static spec.
//
// It does a real round-trip (upload + delete) against a
// quota-limited test user. Catches wire format issues, controller-
// layer enforcement, and persistence-path behavior that no static
// spec can model.
func runSmoke(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "smoke: target required (only \"immich\" supported)")
		return 2
	}
	target := args[0]
	args = args[1:]

	switch target {
	case "immich":
		return runSmokeImmich(ctx, cfg, logger, args)
	default:
		fmt.Fprintf(os.Stderr, "smoke: unknown target %q (only \"immich\" supported)\n", target)
		return 2
	}
}

// smokeImmichConfig collects the credentials the smoke needs.
// Resolution priority: explicit flags, then the IMMICH_BAIRN_*
// CI-side group variables, then the IMMICH_* runtime variables
// bairn fetch uses.
type smokeImmichConfig struct {
	host     string
	user     string
	password string
	apiKey   string
}

func resolveSmokeImmichConfig(cfg *config.Config, hostFlag, userFlag, passwordFlag, apiKeyFlag string) smokeImmichConfig {
	c := smokeImmichConfig{
		host:     firstSet(hostFlag, os.Getenv("IMMICH_BAIRN_HOST"), cfg.ImmichBaseURL),
		user:     firstSet(userFlag, os.Getenv("IMMICH_BAIRN_USER")),
		password: firstSet(passwordFlag, os.Getenv("IMMICH_BAIRN_PASSWORD")),
		apiKey:   firstSet(apiKeyFlag, cfg.ImmichAPIKey),
	}
	c.host = strings.TrimSuffix(strings.TrimRight(c.host, "/"), "/api")
	return c
}

func firstSet(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func runSmokeImmich(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fs := flag.NewFlagSet("smoke immich", flag.ExitOnError)
	hostFlag := fs.String("host", "", "Immich base URL (default: $IMMICH_BAIRN_HOST or $IMMICH_BASE_URL)")
	userFlag := fs.String("user", "", "Immich account email for login flow (default: $IMMICH_BAIRN_USER)")
	passwordFlag := fs.String("password", "", "Immich account password (default: $IMMICH_BAIRN_PASSWORD)")
	apiKeyFlag := fs.String("api-key", "", "Immich API key (default: $IMMICH_API_KEY); ignored when user+password are set")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	sc := resolveSmokeImmichConfig(cfg, *hostFlag, *userFlag, *passwordFlag, *apiKeyFlag)
	if sc.host == "" {
		fmt.Fprintln(os.Stderr, "smoke immich: host required (--host or IMMICH_BAIRN_HOST or IMMICH_BASE_URL)")
		return 2
	}
	if sc.user == "" && sc.apiKey == "" {
		fmt.Fprintln(os.Stderr, "smoke immich: need either user+password (IMMICH_BAIRN_USER+PASSWORD) or api-key (IMMICH_API_KEY)")
		return 2
	}

	logger.Info("smoke", "target", "immich", "host", sc.host, "mode", "round-trip")

	httpClient := smokeHTTPClient()

	// Authenticate. With user+password the round trip is the album
	// leg: it mints a key holding only fetch's album scopes and cleans
	// up with the login session. With an apiKey alone, use it directly for the plain round trip.
	if sc.user != "" && sc.password != "" {
		token, err := contract.ImmichLogin(ctx, httpClient, sc.host, sc.user, sc.password)
		if err != nil {
			logger.Error("smoke", "phase", "login", "err", err)
			return 1
		}
		return runSmokeImmichAlbums(ctx, httpClient, sc.host, token, os.Getenv("SMOKE_SHARE_USER_EMAIL"), logger)
	}

	return runSmokeImmichRoundTrip(ctx, httpClient, sc.host, sc.apiKey, logger)
}

// runSmokeImmichRoundTrip uploads a tiny test asset via bairn's
// production Upload code path, asserts the response, then deletes
// the asset. Catches wire format issues, controller validation,
// and the persistence path in one step.
//
// The production immich.Client expects its baseURL to include the
// /api prefix (operators set IMMICH_BASE_URL=https://host/api).
// Smoke takes the bare host (matches IMMICH_BAIRN_HOST shape) and
// appends /api here so the production code path is exercised
// exactly as in fetch.
func runSmokeImmichRoundTrip(ctx context.Context, client *http.Client, host, apiKey string, logger *slog.Logger) int {
	apiBaseURL := strings.TrimRight(host, "/") + "/api"
	ic := immich.New(apiBaseURL, apiKey, immich.WithHTTPClient(client), immich.WithLogger(logger))

	// Minimal but valid JPEG: 2-byte SOI + JFIF marker + EOI.
	// Just enough that file-type detection in Immich won't reject
	// before the validator runs.
	testID := fmt.Sprintf("bairn-smoke-%d", time.Now().Unix())
	now := time.Now().UTC()
	res, err := ic.Upload(ctx, immich.UploadInput{
		Data:           []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xFF, 0xD9},
		Filename:       testID + ".jpg",
		FileCreatedAt:  now,
		FileModifiedAt: now,
		Metadata:       map[string]string{"bairnSmoke": "true"},
	})
	if err != nil {
		logger.Error("smoke", "phase", "upload", "err", err)
		return 1
	}
	logger.Info("smoke", "phase", "upload", "asset_id", res.ID, "status", res.Status, "duplicate", res.Duplicate)

	// Always try to delete, even if the upload reported "duplicate"
	// (test asset id should be unique per second; if dup, something
	// is leaking — clean up regardless).
	if res.ID != "" {
		if err := contract.DeleteImmichAsset(ctx, client, host, apiKey, res.ID); err != nil {
			logger.Error("smoke", "phase", "cleanup-asset", "asset_id", res.ID, "err", err)
			// Cleanup failure is a real problem (quota leak); fail.
			return 1
		}
		logger.Info("smoke", "phase", "cleanup-asset", "asset_id", res.ID, "result", "deleted")
	}

	logger.Info("smoke", "result", "ok", "phase", "complete")
	return 0
}

// smokeHTTPClient carries the Immich password, session token and minted
// API key, so it refuses cross-host and downgrade redirects.
func smokeHTTPClient() *http.Client { return safehttp.NewClient(30 * time.Second) }
