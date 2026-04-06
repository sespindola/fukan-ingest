package oauth2

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultOpenSkyTokenURL is the Keycloak token endpoint for OpenSky Network.
const DefaultOpenSkyTokenURL = "https://auth.opensky-network.org/auth/realms/opensky-network/protocol/openid-connect/token"

// tokenResponse is the OAuth2 token endpoint response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"` // seconds
	TokenType   string `json:"token_type"`
}

// TokenSource manages OAuth2 client credentials tokens with automatic refresh.
type TokenSource struct {
	clientID     string
	clientSecret string
	tokenURL     string
	client       *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewTokenSource creates a token source for the OAuth2 client credentials flow.
func NewTokenSource(clientID, clientSecret, tokenURL string) *TokenSource {
	return &TokenSource{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenURL:     tokenURL,
		client:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Token returns a valid access token, refreshing if expired or about to expire.
func (ts *TokenSource) Token() (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Return cached token if still valid with 60s margin.
	if ts.token != "" && time.Now().Add(60*time.Second).Before(ts.expiry) {
		return ts.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {ts.clientID},
		"client_secret": {ts.clientSecret},
	}

	resp, err := ts.client.Post(ts.tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oauth2 token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("oauth2 read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth2 token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("oauth2 unmarshal token: %w", err)
	}

	ts.token = tok.AccessToken
	ts.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)

	return ts.token, nil
}
