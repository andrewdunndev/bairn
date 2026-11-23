package immich

import (
	"context"
	"fmt"
	"net/http"
)

// MaxBulkCheck is the most items one BulkUploadCheck call may carry.
// Immich v3.4.2 sets no limit on the request (AssetBulkUploadCheckSchema,
// server/src/dtos/asset-media.dto.ts:72-76); bairn bounds it so one
// request stays small and a failed batch costs little to redo.
const MaxBulkCheck = 500

// CheckItem is one asset to look up: a caller-chosen ID the server
// echoes back, and the hex SHA-1 of the bytes. The server decodes a
// 28-character checksum as base64 and anything else as hex
// (server/src/utils/request.ts:4-6), so a 40-character hex digest goes
// as is.
type CheckItem struct {
	ID       string
	Checksum string
}

// CheckResult is Immich's answer for one CheckItem.
type CheckResult struct {
	ID string
	// Duplicate is true when the owner holds an asset with the checksum
	// (action "reject", reason "duplicate"); false means "accept": the
	// checksum is unknown to the owner.
	Duplicate bool
	// AssetID and IsTrashed describe the existing asset; set only when
	// Duplicate.
	AssetID   string
	IsTrashed bool
}

// BulkUploadCheck asks which checksums the key's owner already holds:
// POST /assets/bulk-upload-check, permission asset.upload
// (server/src/controllers/asset-media.controller.ts:180-181). The
// request is {"assets":[{"id","checksum"}]} (AssetBulkUploadCheckDto)
// and the reply {"results":[{"id","action","reason","assetId",
// "isTrashed"}]} (AssetBulkUploadCheckResponseDto,
// server/src/dtos/asset-media-response.dto.ts:33-48); the service
// keeps trashed rows in the lookup, so isTrashed is meaningful
// (server/src/services/asset-media.service.ts:321-349).
//
// The call is read-only and retried by the shared policy. The reply
// must carry exactly one well-formed result per requested ID, in any
// order; anything else is an error and no result is returned, so a
// malformed answer is never read as "absent".
func (c *Client) BulkUploadCheck(ctx context.Context, items []CheckItem) ([]CheckResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > MaxBulkCheck {
		return nil, fmt.Errorf("immich: bulk-upload-check: %d items exceeds the batch limit %d", len(items), MaxBulkCheck)
	}
	type reqItem struct {
		ID       string `json:"id"`
		Checksum string `json:"checksum"`
	}
	in := struct {
		Assets []reqItem `json:"assets"`
	}{make([]reqItem, len(items))}
	want := make(map[string]struct{}, len(items))
	for i, it := range items {
		if it.ID == "" || it.Checksum == "" {
			return nil, fmt.Errorf("immich: bulk-upload-check: item %d has an empty id or checksum", i)
		}
		if _, dup := want[it.ID]; dup {
			return nil, fmt.Errorf("immich: bulk-upload-check: duplicate id in batch")
		}
		want[it.ID] = struct{}{}
		in.Assets[i] = reqItem(it)
	}
	var out struct {
		Results []struct {
			ID        string `json:"id"`
			Action    string `json:"action"`
			Reason    string `json:"reason"`
			AssetID   string `json:"assetId"`
			IsTrashed bool   `json:"isTrashed"`
		} `json:"results"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/assets/bulk-upload-check", in, &out, true); err != nil {
		return nil, err
	}
	if len(out.Results) != len(items) {
		return nil, fmt.Errorf("immich: bulk-upload-check: %d results for %d items", len(out.Results), len(items))
	}
	res := make([]CheckResult, 0, len(items))
	for _, r := range out.Results {
		if _, ok := want[r.ID]; !ok {
			return nil, fmt.Errorf("immich: bulk-upload-check: result for an id not requested")
		}
		delete(want, r.ID)
		switch {
		case r.Action == "accept":
			res = append(res, CheckResult{ID: r.ID})
		case r.Action == "reject" && r.Reason == "duplicate" && r.AssetID != "":
			res = append(res, CheckResult{ID: r.ID, Duplicate: true, AssetID: r.AssetID, IsTrashed: r.IsTrashed})
		default:
			return nil, fmt.Errorf("immich: bulk-upload-check: unexpected result (action %q, reason %q)", r.Action, r.Reason)
		}
	}
	return res, nil
}
