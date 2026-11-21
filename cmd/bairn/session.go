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
// to the session file. The env token is never written. readOnly (dry
// runs, drift) reuses the file but never writes it; any failure to read
// or write it is logged and the run carries on with a password login.
func sessionTokenSource(cfg *config.Config, readOnly bool, logger *slog.Logger) *famly.RefreshingToken {
	store := session.Open(cfg.SessionPath, readOnly)
	cur, err := store.Load()
	if err != nil {
		logger.Warn("famly session file ignored", "err", err)
	}
	deviceID := cfg.FamlyDeviceID
	rt := famly.NewRefreshingToken(cfg.FamlyEmail, cfg.FamlyPassword, deviceID,
		func(ctx context.Context, email, password, _ string) (string, error) {
			cred, err := famly.LoginDevice(ctx, cfg.FamlyBaseURL, email, password, deviceID)
			if err != nil {
				return "", err
			}
			cur.AccessToken = cred.AccessToken
			if err := store.Save(cur); err != nil {
				logger.Warn("could not save famly session; the next run will log in again", "err", err)
			}
			return cred.AccessToken, nil
		})
	if cfg.FamlyAccessToken != "" {
		rt.Seed(cfg.FamlyAccessToken)
	} else {
		rt.Seed(cur.AccessToken)
	}
	return rt
}
