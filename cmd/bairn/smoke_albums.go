package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/albums"
	"gitlab.com/dunn.dev/bairn/internal/contract"
)

// smokeAlbumPermissions is the whole scope of fetch's API key. The
// smoke mints exactly this, so a pass proves the narrow key suffices.
var smokeAlbumPermissions = []string{"asset.upload", "album.read", "album.create", "albumAsset.create"}

// smokeChecks prints one PASS/FAIL line per assertion.
type smokeChecks struct{ failed int }

func (c *smokeChecks) check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("PASS %s\n", name)
		return
	}
	c.failed++
	fmt.Printf("FAIL %s: %s\n", name, detail)
}

type smokeAlbum struct {
	ID         string   `json:"id"`
	AlbumName  string   `json:"albumName"`
	AssetCount int      `json:"assetCount"`
	Members    []string `json:"-"`
	// Immich 3.4.2 AlbumResponseDto has no ownerId or assets: the owner
	// is the albumUsers entry with role "owner", and membership is read
	// with a metadata search scoped to the album.
	AlbumUsers []struct {
		Role string `json:"role"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"albumUsers"`
}

// smokeAlbumAsset returns bytes of a minimal JPEG made unique per run
// by a comment segment, so the first upload is never a duplicate of an
// asset the smoke does not own.
func smokeAlbumAsset() []byte {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00}
	b = append(b, 0xFF, 0xFE, 0x00, byte(2+len(nonce)))
	b = append(b, nonce...)
	return append(b, 0xFF, 0xD9)
}

// runSmokeImmichAlbums exercises album filing the way fetch does,
// through the albums filer with a key holding only the album scopes,
// then reads the result back and cleans up with the login session.
func runSmokeImmichAlbums(ctx context.Context, client *http.Client, host, token, shareEmail string, logger *slog.Logger) (code int) {
	var cs smokeChecks
	var cleanups []func(context.Context)
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i](cctx)
		}
		if cs.failed > 0 {
			code = 1
		}
	}()
	fail := func(phase string, err error) int {
		logger.Error("smoke", "phase", phase, "err", err)
		return 1
	}

	var me struct {
		ID string `json:"id"`
	}
	if err := contract.ImmichSessionJSON(ctx, client, host, token, http.MethodGet, "/api/users/me", nil, &me); err != nil || me.ID == "" {
		return fail("whoami", fmt.Errorf("resolve key owner: %v", err))
	}
	var shareIDs []string
	shareID := ""
	if shareEmail != "" {
		var users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if err := contract.ImmichSessionJSON(ctx, client, host, token, http.MethodGet, "/api/users", nil, &users); err != nil {
			return fail("share-user", err)
		}
		for _, u := range users {
			if strings.EqualFold(u.Email, shareEmail) {
				shareID = u.ID
			}
		}
		if shareID == "" {
			return fail("share-user", fmt.Errorf("no Immich user matches SMOKE_SHARE_USER_EMAIL"))
		}
		shareIDs = []string{shareID}
	}

	key, err := contract.MintImmichAPIKey(ctx, client, host, token, fmt.Sprintf("bairn-smoke-%d", time.Now().Unix()), smokeAlbumPermissions)
	if err != nil {
		return fail("mint-api-key", err)
	}
	cleanups = append(cleanups, func(c context.Context) {
		if err := contract.DeleteImmichAPIKey(c, client, host, token, key.ID); err != nil {
			logger.Warn("smoke", "phase", "cleanup-api-key", "key_id", key.ID, "err", err)
			cs.failed++
		} else {
			logger.Info("smoke", "phase", "cleanup-api-key", "key_id", key.ID, "result", "deleted")
		}
	})

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	prefix := "bairn-smoke-" + hex.EncodeToString(suffix)
	now := time.Now().UTC()
	cfg := albums.Config{MonthPattern: prefix + " %B %Y", YearPattern: prefix + " %Y", ShareUserIDs: shareIDs, Zone: time.UTC}
	names := albums.New(nil, cfg, nil).Names(now)

	cleanups = append(cleanups, func(c context.Context) {
		var all []smokeAlbum
		if err := contract.ImmichSessionJSON(c, client, host, token, http.MethodGet, "/api/albums", nil, &all); err != nil {
			logger.Warn("smoke", "phase", "cleanup-albums", "err", err)
			cs.failed++
			return
		}
		for _, a := range all {
			if !strings.HasPrefix(a.AlbumName, prefix) {
				continue
			}
			if err := contract.ImmichSessionJSON(c, client, host, token, http.MethodDelete, "/api/albums/"+url.PathEscape(a.ID), nil, nil); err != nil {
				logger.Warn("smoke", "phase", "cleanup-album", "album_id", a.ID, "err", err)
				cs.failed++
			} else {
				logger.Info("smoke", "phase", "cleanup-album", "album_id", a.ID, "result", "deleted")
			}
		}
		var left []smokeAlbum
		if err := contract.ImmichSessionJSON(c, client, host, token, http.MethodGet, "/api/albums", nil, &left); err != nil {
			logger.Warn("smoke", "phase", "cleanup-verify", "err", err)
			cs.failed++
			return
		}
		remaining := 0
		for _, a := range left {
			if strings.HasPrefix(a.AlbumName, "bairn-smoke-") {
				remaining++
			}
		}
		if remaining > 0 {
			logger.Warn("smoke", "phase", "cleanup-verify", "remaining", remaining)
			cs.failed++
		} else {
			logger.Info("smoke", "phase", "cleanup-verify", "result", "no bairn-smoke- albums remain")
		}
	})

	ic := immich.New(strings.TrimRight(host, "/")+"/api", key.Secret, immich.WithHTTPClient(client), immich.WithLogger(logger))
	data := smokeAlbumAsset()
	upload := func() (*immich.UploadResult, error) {
		res, err := ic.Upload(ctx, immich.UploadInput{
			Data: data, Filename: prefix + ".jpg", FileCreatedAt: now, FileModifiedAt: now,
			Metadata: map[string]string{"bairnSmoke": "true"},
		})
		if err == nil && res.ID != "" && !res.Duplicate {
			assetID := res.ID
			cleanups = append(cleanups, func(c context.Context) {
				if err := contract.ImmichSessionJSON(c, client, host, token, http.MethodDelete, "/api/assets", map[string]any{"ids": []string{assetID}, "force": true}, nil); err != nil {
					logger.Warn("smoke", "phase", "cleanup-asset", "asset_id", assetID, "err", err)
					cs.failed++
				} else {
					logger.Info("smoke", "phase", "cleanup-asset", "asset_id", assetID, "result", "deleted")
				}
			})
		}
		return res, err
	}
	fileOnce := func() (*immich.UploadResult, error) {
		res, err := upload()
		if err != nil {
			return res, fmt.Errorf("upload: %w", err)
		}
		f := albums.New(ic, cfg, logger)
		if err := f.Add(ctx, res.ID, now); err != nil {
			return res, err
		}
		return res, f.Flush(ctx)
	}
	// members lists asset ids in an album via the admin session; 3.4.2
	// has no assets on GET /albums/{id}.
	members := func(albumID string) ([]string, error) {
		var out struct {
			Assets struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			} `json:"assets"`
		}
		body := map[string]any{"albumIds": []string{albumID}, "size": 1000}
		if err := contract.ImmichSessionJSON(ctx, client, host, token, http.MethodPost, "/api/search/metadata", body, &out); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(out.Assets.Items))
		for _, x := range out.Assets.Items {
			ids = append(ids, x.ID)
		}
		return ids, nil
	}
	snapshot := func() map[string][]smokeAlbum {
		out := map[string][]smokeAlbum{}
		var all []smokeAlbum
		if err := contract.ImmichSessionJSON(ctx, client, host, token, http.MethodGet, "/api/albums", nil, &all); err != nil {
			cs.check("list albums", false, err.Error())
			return out
		}
		for _, a := range all {
			for _, n := range names {
				if a.AlbumName == n {
					var full smokeAlbum
					if err := contract.ImmichSessionJSON(ctx, client, host, token, http.MethodGet, "/api/albums/"+url.PathEscape(a.ID), nil, &full); err != nil {
						cs.check("read album", false, err.Error())
						continue
					}
					ids, err := members(a.ID)
					if err != nil {
						cs.check("read album members", false, err.Error())
						continue
					}
					full.Members = ids
					out[n] = append(out[n], full)
				}
			}
		}
		return out
	}

	res1, err := fileOnce()
	cs.check("leg1 upload and file with narrow key", err == nil, fmt.Sprint(err))
	assetID := ""
	if res1 != nil {
		assetID = res1.ID
		cs.check("leg1 upload is new", !res1.Duplicate, "server answered duplicate")
	}
	if err == nil {
		snap := snapshot()
		for _, n := range names {
			as := snap[n]
			cs.check("leg1 one album "+n, len(as) == 1, fmt.Sprintf("found %d", len(as)))
			if len(as) != 1 {
				continue
			}
			a := as[0]
			owned := false
			for _, u := range a.AlbumUsers {
				owned = owned || (u.User.ID == me.ID && u.Role == "owner")
			}
			cs.check("leg1 owned by key owner "+n, owned, "owner differs")
			has := slices.Contains(a.Members, assetID)
			cs.check("leg1 contains asset "+n, has, "asset missing")
			if shareID != "" {
				editor := false
				for _, u := range a.AlbumUsers {
					editor = editor || (u.User.ID == shareID && u.Role == immich.AlbumRoleEditor)
				}
				cs.check("leg1 share user is editor "+n, editor, "share user not an editor")
			}
		}
	}

	before := snapshot()
	res2, err := fileOnce()
	cs.check("leg2 rerun upload and file", err == nil, fmt.Sprint(err))
	if err == nil {
		cs.check("leg2 upload is duplicate of the same asset", res2 != nil && res2.Duplicate && res2.ID == assetID, "rerun did not dedupe")
		after := snapshot()
		for _, n := range names {
			cs.check("leg2 still one album "+n, len(after[n]) == 1, fmt.Sprintf("found %d", len(after[n])))
			if len(after[n]) == 1 && len(before[n]) == 1 {
				cs.check("leg2 asset count unchanged "+n, len(after[n][0].Members) == len(before[n][0].Members),
					fmt.Sprintf("%d -> %d", len(before[n][0].Members), len(after[n][0].Members)))
			}
		}
	}

	if cs.failed > 0 {
		fmt.Fprintf(os.Stderr, "smoke immich: %d album assertion(s) failed\n", cs.failed)
		return 1
	}
	logger.Info("smoke", "result", "ok", "phase", "albums")
	return 0
}
