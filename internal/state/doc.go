// Package state persists per-asset progress across runs. SQLite,
// two-phase rows: vendor side (downloaded_at) and Immich side
// (uploaded_at, immich_asset_id). Single-process; concurrent runs
// are not supported.
package state
