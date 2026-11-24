package sink

import (
	"context"
	"errors"
)

// ErrUnauthorized is returned by a Confirmer whose remote rejected its
// credentials. No later batch can succeed, so callers stop.
var ErrUnauthorized = errors.New("sink: unauthorized")

// Outcome is a Confirmer's verdict on one entry.
type Outcome int

const (
	// Absent: the remote does not hold the bytes.
	Absent Outcome = iota
	// Held: the remote holds the bytes under the entry's own remote id
	// and has not trashed them.
	Held
	// Trashed: the remote holds the bytes under the entry's own remote
	// id but has trashed them.
	Trashed
	// Mismatch: the remote holds the bytes under a different remote id.
	Mismatch
)

// ConfirmItem is one entry to confirm: a caller-chosen ID echoed back in
// the result, the remote id the sink returned at upload (Receipt.DestPath),
// and the hex SHA-1 of the bytes uploaded.
type ConfirmItem struct {
	ID       string
	RemoteID string
	SHA1     string
}

// ConfirmResult is the verdict for the ConfirmItem with the same ID.
type ConfirmResult struct {
	ID      string
	Outcome Outcome
}

// Confirmer is an optional Sink capability: asking the remote whether it
// still holds the bytes it was given. verify and prune need one; the
// disk sink is not one.
type Confirmer interface {
	// Confirm answers for at most ConfirmBatch items per call.
	Confirm(ctx context.Context, items []ConfirmItem) ([]ConfirmResult, error)
	// ConfirmBatch is the most items one Confirm call may carry.
	ConfirmBatch() int
}
