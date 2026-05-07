package famly

import (
	"context"
	"net/http"
	"net/url"
)

// Me fetches the caller's own profile. The response carries the
// caller's LoginID (used to identify "liked-by-me" in feed filters)
// and the list of children the caller is enrolled against.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.do(ctx, http.MethodGet, "/api/me/me/me", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Relations fetches the caregiver list for a given child. bairn
// uses this to gather the household login IDs for the
// "liked-by-anyone-in-the-household" filter.
func (c *Client) Relations(ctx context.Context, childID string) ([]Relation, error) {
	q := url.Values{}
	q.Set("childId", childID)
	var out []Relation
	if err := c.do(ctx, http.MethodGet, "/api/v2/relations", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
