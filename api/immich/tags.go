package immich

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// The tag surface bairn uses, with the API-key permission each call
// needs at the release pinned as IMMICH_VERSION (the spec's
// x-immich-permission for the operation):
//
//	PUT /tags               tag.create   (upsertTags: create the tags that are missing, return all)
//	PUT /tags/{id}/assets   tag.asset    (tagAssets: add a tag to assets)
//
// UpsertTag is find-or-create in one idempotent call, so bairn never
// needs tag.read, and no method here can remove a tag from an asset or
// delete a tag (those are tag.asset on DELETE and tag.delete).

// UpsertTag returns the ID of the tag the key's owner has under name,
// creating it when absent. A tag already there is returned as is.
func (c *Client) UpsertTag(ctx context.Context, name string) (string, error) {
	in := struct {
		Tags []string `json:"tags"`
	}{[]string{name}}
	var out []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := c.doJSON(ctx, http.MethodPut, "/tags", in, &out, true); err != nil {
		return "", err
	}
	for _, t := range out {
		if t.Value == name || t.Name == name {
			if t.ID == "" {
				break
			}
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("immich: upsert tag %q: response did not carry the tag", name)
}

// TagAssets adds the tag to assets and returns the assets Immich
// refused, mapped to its per-item reason, with the same rules as
// AddAssetsToAlbum: an asset already tagged is "duplicate" and counts as
// done, "no_permission" and "not_found" are final, and any other reason
// is returned as an error so the batch is retried.
func (c *Client) TagAssets(ctx context.Context, tagID string, assetIDs []string) (map[string]string, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	in := struct {
		IDs []string `json:"ids"`
	}{assetIDs}
	var out []bulkResult
	if err := c.doJSON(ctx, http.MethodPut, "/tags/"+url.PathEscape(tagID)+"/assets", in, &out, true); err != nil {
		return nil, err
	}
	return refusals(out, "tag assets")
}
