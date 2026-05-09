package main

import (
	"context"
	"encoding/json"
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
)

// runSmoke implements the smoke subcommand: a tag-time gate that
// verifies a sink's wire contract against a live server. Today only
// the immich target is supported; the same shape applies to any
// future sink whose contract isn't fully captured by a static spec.
//
// Default mode does a real round-trip (upload + delete) against a
// quota-limited test user. Catches wire format issues, controller-
// layer enforcement, and persistence-path behavior that no static
// spec can model.
//
// --probe-only sends a deliberately-incomplete request and parses
// the validator's rejection. Non-destructive. Used when the live
// server doesn't permit writes (public demos, audit modes) or when
// capturing the required-field set for a static manifest.
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
	probeOnly := fs.Bool("probe-only", false, "validator probe only (no upload). Use when the live server doesn't permit writes or for manifest capture")
	capturePath := fs.String("capture", "", "with --probe-only, write the captured required-field manifest to this path")
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

	logger.Info("smoke", "target", "immich", "host", sc.host, "mode", smokeMode(*probeOnly))

	httpClient := &http.Client{Timeout: 30 * time.Second}

	// Authenticate. If user+password, do the login + mint flow so
	// the rest of the gate runs through the production x-api-key
	// code path. If apiKey, use it directly.
	apiKey := sc.apiKey
	var minted *contract.ImmichAPIKey
	var token string
	if sc.user != "" && sc.password != "" {
		var err error
		token, err = contract.ImmichLogin(ctx, httpClient, sc.host, sc.user, sc.password)
		if err != nil {
			logger.Error("smoke", "phase", "login", "err", err)
			return 1
		}
		permissions := []string{"asset.upload", "asset.delete"}
		if *probeOnly {
			permissions = []string{"asset.upload"}
		}
		minted, err = contract.MintImmichAPIKey(ctx, httpClient, sc.host, token, fmt.Sprintf("bairn-smoke-%d", time.Now().Unix()), permissions)
		if err != nil {
			logger.Error("smoke", "phase", "mint-api-key", "err", err)
			return 1
		}
		apiKey = minted.Secret
		defer func() {
			if err := contract.DeleteImmichAPIKey(context.Background(), httpClient, sc.host, token, minted.ID); err != nil {
				logger.Warn("smoke", "phase", "cleanup-api-key", "key_id", minted.ID, "err", err)
			} else {
				logger.Info("smoke", "phase", "cleanup-api-key", "key_id", minted.ID, "result", "deleted")
			}
		}()
	}

	if *probeOnly {
		return runSmokeImmichProbe(ctx, httpClient, sc.host, apiKey, *capturePath, logger)
	}
	return runSmokeImmichRoundTrip(ctx, httpClient, sc.host, apiKey, logger)
}

func smokeMode(probeOnly bool) string {
	if probeOnly {
		return "probe-only"
	}
	return "round-trip"
}

// runSmokeImmichProbe is the non-destructive validator probe.
// Optionally writes the captured manifest to a path.
func runSmokeImmichProbe(ctx context.Context, client *http.Client, host, apiKey, capturePath string, logger *slog.Logger) int {
	manifest, err := contract.ProbeImmichUploadRequiredFields(ctx, client, host, apiKey)
	if err != nil {
		logger.Error("smoke", "phase", "probe", "err", err)
		return 1
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		logger.Error("smoke", "phase", "marshal", "err", err)
		return 1
	}
	body = append(body, '\n')

	if capturePath == "" {
		_, _ = os.Stdout.Write(body)
		return 0
	}
	if err := os.WriteFile(capturePath, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "smoke: write:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", capturePath)
	return 0
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
		DeviceID:       "bairn-smoke",
		DeviceAssetID:  testID,
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
