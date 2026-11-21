package main

import (
	"context"
	"log/slog"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/session"
)

// sessionTokenSource builds the credential-backed token source with
// session reuse: it is seeded with the token from FAMLY_ACCESS_TOKEN,
// else from the session file, and every token it mints is written back
// to the session file, except while FAMLY_ACCESS_TOKEN is set, when no
// token is written at all. readOnly (dry
// runs, drift) reuses the file but never writes it; any failure to read
// or write it is logged and the run carries on with a password login.
// seed false skips the seeding: the first Token call logs in fresh, for
// callers (the drift gate) that use the token as a plain string and
// would never see a 401 to heal a stale one.
func sessionTokenSource(cfg *config.Config, readOnly, seed bool, logger *slog.Logger) *famly.RefreshingToken {
	store := session.Open(cfg.SessionPath, readOnly)
	cur, err := store.Load()
	if err != nil {
		logger.Warn("famly session file ignored", "err", err)
	}
	deviceID := resolveDeviceID(cfg, store, &cur, logger)
	rt := famly.NewRefreshingToken(cfg.FamlyEmail, cfg.FamlyPassword, deviceID,
		func(ctx context.Context, email, password, _ string) (string, error) {
			cred, err := famly.LoginDevice(ctx, cfg.FamlyBaseURL, email, password, deviceID)
			if err != nil {
				return "", err
			}
			// While FAMLY_ACCESS_TOKEN is set the file stays out of the
			// token business: a token minted after the env one was
			// rejected is used for this run only.
			saveToken := cfg.FamlyAccessToken == ""
			if saveToken {
				cur.AccessToken = cred.AccessToken
			}
			// Famly may report the id it registered; keep that one,
			// unless the operator pinned one with FAMLY_DEVICE_ID.
			idChanged := false
			if cfg.FamlyDeviceID == "" && famly.ValidDeviceID(cred.DeviceID) && cred.DeviceID != deviceID {
				deviceID, cur.DeviceID = cred.DeviceID, cred.DeviceID
				idChanged = true
			}
			if !saveToken && !idChanged {
				return cred.AccessToken, nil
			}
			if err := store.Save(cur); err != nil {
				logger.Warn("could not save famly session; the next run will log in again", "err", err)
			}
			return cred.AccessToken, nil
		})
	switch {
	case !seed:
	case cfg.FamlyAccessToken != "":
		rt.Seed(cfg.FamlyAccessToken)
	default:
		rt.Seed(cur.AccessToken)
	}
	return rt
}

// resolveDeviceID picks the device id sent to Famly: FAMLY_DEVICE_ID,
// else the one in the session file, else a new random UUID that is
// stored at once so every later run sends the same one. A pinned env id
// is never written to the file. A failure to store only costs a
// new-device notice next run, so it is logged, not fatal.
func resolveDeviceID(cfg *config.Config, store *session.Store, cur *session.Session, logger *slog.Logger) string {
	if cfg.FamlyDeviceID != "" {
		return cfg.FamlyDeviceID
	}
	if famly.ValidDeviceID(cur.DeviceID) {
		return cur.DeviceID
	}
	id, err := famly.NewDeviceID()
	if err != nil {
		// crypto/rand failed; nothing sensible to send but a fixed shape.
		logger.Error("device id", "err", err)
		return "00000000-0000-4000-8000-000000000000"
	}
	cur.DeviceID = id
	if err := store.Save(*cur); err != nil {
		logger.Warn("could not store the generated device id; Famly may report a new device next run", "err", err)
	}
	return id
}
