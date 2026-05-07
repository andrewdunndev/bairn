package sink

import (
	"context"
	"fmt"
	"os"

	"gitlab.com/dunn.dev/bairn/api/immich"
)

// Immich is the optional secondary sink. Activates when
// IMMICH_BASE_URL and IMMICH_API_KEY are configured. Uses the
// already-saved-on-disk file rather than re-downloading from
// Famly: by the time Immich runs, Disk has already saved.
type Immich struct {
	client *immich.Client
}

// NewImmich constructs an Immich sink.
func NewImmich(client *immich.Client) *Immich { return &Immich{client: client} }

// Name implements Sink.
func (Immich) Name() string { return "immich" }

// Put reads the file at in.SourcePath (which Disk has already
// written) and uploads to Immich with the bairn metadata
// preserved as Immich asset metadata.
//
// The duplicate-detection happens server-side via x-immich-checksum;
// the receipt's Status reflects what Immich did.
func (i *Immich) Put(ctx context.Context, in PutInput) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	data, err := os.ReadFile(in.SourcePath)
	if err != nil {
		return Receipt{}, fmt.Errorf("sink: read %s: %w", in.SourcePath, err)
	}
	res, err := i.client.Upload(ctx, immich.UploadInput{
		Data:           data,
		Filename:       in.Filename,
		FileCreatedAt:  in.FileCreatedAt,
		FileModifiedAt: in.FileCreatedAt,
		Metadata: map[string]string{
			"famlyImageId":  in.FamlyImageID,
			"famlyFeedItem": in.FeedItemID,
			"famlySource":   in.Source,
		},
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("sink: upload %s: %w", in.FamlyImageID, err)
	}
	return Receipt{
		DestPath: res.ID,
		Status:   res.Status,
		Size:     int64(len(data)),
	}, nil
}
