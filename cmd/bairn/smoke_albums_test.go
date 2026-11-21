package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

const (
	smokeOwner = "11111111-1111-4111-8111-111111111111"
	smokeShare = "22222222-2222-4222-8222-222222222222"
)

type smokeFake struct {
	perms      []string
	albums     map[string]*smokeAlbum
	members    map[string][]string // album id -> asset ids; 3.4.2 GET /albums/{id} carries none
	nextID     int
	uploads    int
	keyDeleted bool
	assetGone  bool
	badAuth    []string
	failAdds   bool
	dropAdds   bool // add answers success but files nothing

	tags       map[string]string   // tag id -> value
	tagAssets  map[string][]string // tag id -> asset ids
	tagCreates int
	dropTags   bool // tagging answers success but tags nothing
}

func ownerEntry(userID, role string) (e struct {
	Role string `json:"role"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
}) {
	e.Role, e.User.ID = role, userID
	return
}

func newSmokeFake(t *testing.T) (*httptest.Server, *smokeFake) {
	t.Helper()
	f := &smokeFake{albums: map[string]*smokeAlbum{}, members: map[string][]string{},
		tags: map[string]string{}, tagAssets: map[string][]string{}}
	mux := http.NewServeMux()
	// need records which credential a route saw; keyRoutes must carry
	// the API key, session routes the bearer token.
	need := func(r *http.Request, key bool) {
		hasKey := r.Header.Get("x-api-key") == "secret-key"
		hasTok := r.Header.Get("Authorization") == "Bearer tok"
		if key && (!hasKey || hasTok) || !key && (!hasTok || hasKey) {
			f.badAuth = append(f.badAuth, r.Method+" "+r.URL.Path)
		}
	}
	js := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		js(w, map[string]string{"id": smokeOwner})
	})
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		js(w, []map[string]string{{"id": smokeShare, "email": "Share@Example.test"}})
	})
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		var in struct{ Permissions []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.perms = in.Permissions
		js(w, map[string]any{"secret": "secret-key", "apiKey": map[string]string{"id": "key-1"}})
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		f.keyDeleted = true
	})
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		need(r, true)
		status := "created"
		if f.uploads++; f.uploads > 1 {
			status = "duplicate"
		}
		js(w, map[string]string{"id": "asset-1", "status": status})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) { need(r, false); f.assetGone = true })
	mux.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("isOwned") != "" {
			need(r, true)
		} else {
			need(r, false)
		}
		out := []smokeAlbum{}
		for _, a := range f.albums {
			if n := r.URL.Query().Get("name"); n == "" || n == a.AlbumName {
				out = append(out, *a)
			}
		}
		js(w, out)
	})
	mux.HandleFunc("POST /api/albums", func(w http.ResponseWriter, r *http.Request) {
		need(r, true)
		var in struct {
			AlbumName  string
			AlbumUsers []struct {
				UserID string
				Role   string
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.nextID++
		a := &smokeAlbum{ID: fmt.Sprintf("al-%d", f.nextID), AlbumName: in.AlbumName}
		a.AlbumUsers = append(a.AlbumUsers, ownerEntry(smokeOwner, "owner"))
		for _, u := range in.AlbumUsers {
			a.AlbumUsers = append(a.AlbumUsers, ownerEntry(u.UserID, u.Role))
		}
		f.albums[a.ID] = a
		js(w, map[string]string{"id": a.ID})
	})
	mux.HandleFunc("PUT /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		need(r, true)
		if f.failAdds {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var in struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		a := f.albums[r.PathValue("id")]
		res := []map[string]any{}
		for _, id := range in.IDs {
			if id == "00000000-0000-4000-8000-000000000000" {
				res = append(res, map[string]any{"id": id, "success": false, "error": "no_permission"})
				continue
			}
			if slices.Contains(f.members[a.ID], id) {
				res = append(res, map[string]any{"id": id, "error": "duplicate"})
				continue
			}
			if !f.dropAdds {
				f.members[a.ID] = append(f.members[a.ID], id)
			}
			res = append(res, map[string]any{"id": id, "success": true})
		}
		js(w, res)
	})
	mux.HandleFunc("GET /api/albums/{id}", func(w http.ResponseWriter, r *http.Request) { need(r, false); js(w, f.albums[r.PathValue("id")]) })
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		var in struct {
			AlbumIDs []string `json:"albumIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]string{}
		if len(in.AlbumIDs) == 1 {
			for _, id := range f.members[in.AlbumIDs[0]] {
				items = append(items, map[string]string{"id": id})
			}
		}
		js(w, map[string]any{"assets": map[string]any{"items": items, "count": len(items)}})
	})
	mux.HandleFunc("DELETE /api/albums/{id}", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		delete(f.albums, r.PathValue("id"))
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		need(r, true)
		var in struct{ Tags []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		out := []map[string]string{}
		for _, name := range in.Tags {
			id := ""
			for tid, v := range f.tags {
				if v == name {
					id = tid
				}
			}
			if id == "" {
				f.tagCreates++
				id = fmt.Sprintf("tag-%d", f.tagCreates)
				f.tags[id] = name
			}
			out = append(out, map[string]string{"id": id, "name": name, "value": name})
		}
		js(w, out)
	})
	mux.HandleFunc("PUT /api/tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		need(r, true)
		var in struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		tid := r.PathValue("id")
		res := []map[string]any{}
		for _, id := range in.IDs {
			switch {
			case id == "00000000-0000-4000-8000-000000000000":
				res = append(res, map[string]any{"id": id, "success": false, "error": "not_found"})
			case slices.Contains(f.tagAssets[tid], id):
				res = append(res, map[string]any{"id": id, "success": false, "error": "duplicate"})
			default:
				if !f.dropTags {
					f.tagAssets[tid] = append(f.tagAssets[tid], id)
				}
				res = append(res, map[string]any{"id": id, "success": true})
			}
		}
		js(w, res)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		out := []map[string]string{}
		for id, v := range f.tags {
			out = append(out, map[string]string{"id": id, "value": v, "name": v})
		}
		js(w, out)
	})
	mux.HandleFunc("DELETE /api/tags/{id}", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		delete(f.tags, r.PathValue("id"))
	})
	mux.HandleFunc("GET /api/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		need(r, false)
		own := []map[string]string{}
		for tid, ids := range f.tagAssets {
			if slices.Contains(ids, r.PathValue("id")) && f.tags[tid] != "" {
				own = append(own, map[string]string{"id": tid, "value": f.tags[tid]})
			}
		}
		js(w, map[string]any{"id": r.PathValue("id"), "tags": own})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func TestSmokeAlbumLegUsesNarrowKeyAndCleansUp(t *testing.T) {
	srv, f := newSmokeFake(t)
	code := runSmokeImmichAlbums(context.Background(), srv.Client(), srv.URL, "tok", "share@example.test", slog.New(slog.DiscardHandler))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := []string{"asset.upload", "album.read", "album.create", "albumAsset.create", "tag.create", "tag.asset"}
	if !slices.Equal(f.perms, want) {
		t.Errorf("key permissions = %v, want exactly %v", f.perms, want)
	}
	if len(f.albums) != 0 || !f.assetGone || !f.keyDeleted {
		t.Errorf("cleanup incomplete: albums=%d asset=%v key=%v", len(f.albums), f.assetGone, f.keyDeleted)
	}
	if f.tagCreates != 1 {
		t.Errorf("tag created %d times, want once", f.tagCreates)
	}
	if len(f.tags) != 0 {
		t.Errorf("smoke tag not deleted by cleanup: %v", f.tags)
	}
	if len(f.badAuth) > 0 {
		t.Errorf("wrong credential on: %s", strings.Join(f.badAuth, ", "))
	}
}

func TestSmokeAlbumLegFailsWhenFilerNeverAddsAsset(t *testing.T) {
	srv, f := newSmokeFake(t)
	f.dropAdds = true
	if code := runSmokeImmichAlbums(context.Background(), srv.Client(), srv.URL, "tok", "share@example.test", slog.New(slog.DiscardHandler)); code == 0 {
		t.Fatal("want failure when albums hold no asset")
	}
}

func TestSmokeAlbumLegCleansUpAfterFailure(t *testing.T) {
	srv, f := newSmokeFake(t)
	f.failAdds = true
	code := runSmokeImmichAlbums(context.Background(), srv.Client(), srv.URL, "tok", "share@example.test", slog.New(slog.DiscardHandler))
	if code == 0 {
		t.Fatal("want failure when the key cannot add to albums")
	}
	if len(f.albums) != 0 || !f.assetGone || !f.keyDeleted {
		t.Errorf("cleanup skipped on failure: albums=%d asset=%v key=%v", len(f.albums), f.assetGone, f.keyDeleted)
	}
}

func TestSmokeAlbumLegRefusesUnknownShareUserBeforeMutating(t *testing.T) {
	srv, f := newSmokeFake(t)
	if code := runSmokeImmichAlbums(context.Background(), srv.Client(), srv.URL, "tok", "nobody@example.test", slog.New(slog.DiscardHandler)); code == 0 {
		t.Fatal("want failure for unknown share user")
	}
	if f.keyDeleted || f.uploads != 0 || f.perms != nil {
		t.Errorf("mutated before the share user resolved: perms=%v uploads=%d", f.perms, f.uploads)
	}
}

func TestSmokeTagLegFailsWhenTagNeverApplies(t *testing.T) {
	srv, f := newSmokeFake(t)
	f.dropTags = true
	if code := runSmokeImmichAlbums(context.Background(), srv.Client(), srv.URL, "tok", "share@example.test", slog.New(slog.DiscardHandler)); code == 0 {
		t.Fatal("want failure when the asset does not carry the tag")
	}
	if len(f.tags) != 0 || !f.keyDeleted {
		t.Errorf("cleanup skipped on tag failure: tags=%v key=%v", f.tags, f.keyDeleted)
	}
}
