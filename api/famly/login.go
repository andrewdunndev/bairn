package famly

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Khan/genqlient/graphql"
)

// Login exchanges email + password for a fresh session token.
//
// This is the canonical implementation of the login callback used
// by RefreshingToken. It hits the Famly Authenticate mutation with
// no auth header (login is unauthenticated by definition) and
// pattern-matches the union response.
//
// Errors are prescriptive: failed-credentials errors include the
// AuthenticationStatus enum value so the operator can tell whether
// the issue is a typo'd password vs an MFA requirement.
func Login(ctx context.Context, baseURL, email, password, deviceID string) (string, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	gqlClient := graphql.NewClient(baseURL+"/graphql", httpClient)

	resp, err := Authenticate(ctx, gqlClient, email, password, deviceID, false)
	if err != nil {
		return "", fmt.Errorf("famly: Authenticate request: %w", err)
	}
	if resp == nil || resp.Me == nil {
		return "", fmt.Errorf("famly: Authenticate: empty response")
	}

	switch r := resp.Me.GetAuthenticateWithPassword().(type) {
	case *AuthenticateMeMeMutationAuthenticateWithPasswordAuthenticationSucceeded:
		return r.AccessToken, nil
	case *AuthenticateMeMeMutationAuthenticateWithPasswordAuthenticationFailed:
		return "", fmt.Errorf("famly: login failed (%s): %s",
			r.Status, r.ErrorTitle)
	case *AuthenticateMeMeMutationAuthenticateWithPasswordAuthenticationChallenged:
		return "", fmt.Errorf("famly: login challenged (%s); MFA flows are not yet supported", r.Status)
	default:
		return "", fmt.Errorf("famly: login returned unexpected type %T", r)
	}
}

// NewRefreshingTokenFromCredentials constructs a RefreshingToken
// that authenticates against the production Famly endpoint via
// Login. baseURL may be empty for the production default.
func NewRefreshingTokenFromCredentials(baseURL, email, password, deviceID string) *RefreshingToken {
	return NewRefreshingToken(email, password, deviceID, func(ctx context.Context, e, p, d string) (string, error) {
		return Login(ctx, baseURL, e, p, d)
	})
}
