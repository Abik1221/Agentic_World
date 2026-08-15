package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubVerifier completes the GitHub OAuth "web application" flow: it exchanges the
// authorization `code` the browser came back with for an access token, then reads the
// user's stable id, login and verified primary email from the GitHub API. Unlike
// Google (a self-verifying ID token), GitHub requires a client SECRET for the code
// exchange, so this holds both halves of the OAuth app credential. stdlib only.
//
// The stable link key is the numeric GitHub user id, NOT the login: a login/username
// can be changed by its owner, the id never does — linking on the login would let a
// renamed account be hijacked by whoever claimed the freed-up name.

const (
	githubTokenURL   = "https://github.com/login/oauth/access_token"
	githubUserURL    = "https://api.github.com/user"
	githubEmailsURL  = "https://api.github.com/user/emails"
	githubUserAgent  = "pyyol-arena"
	githubOAuthScope = "read:user user:email"
)

// GitHubClaims is the subset of a GitHub identity we use, shaped to mirror GoogleClaims.
type GitHubClaims struct {
	// ID is GitHub's stable numeric user id, as a string. The link key.
	ID            string
	Login         string // the @handle (mutable) — used only to seed a default agent name
	Email         string
	EmailVerified bool
	Name          string
}

type GitHubVerifier struct {
	clientID     string
	clientSecret string
	client       *http.Client
}

func NewGitHubVerifier(clientID, clientSecret string) *GitHubVerifier {
	return &GitHubVerifier{
		clientID:     clientID,
		clientSecret: clientSecret,
		client:       &http.Client{Timeout: 8 * time.Second},
	}
}

// Enabled reports whether GitHub login is configured (both id and secret are set).
func (v *GitHubVerifier) Enabled() bool { return v.clientID != "" && v.clientSecret != "" }

// AuthorizeURL is the page the browser is sent to. Exposed so the frontend can be
// pointed here without hardcoding GitHub's endpoint, and so scope stays in one place.
func (v *GitHubVerifier) AuthorizeURL(redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", v.clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", githubOAuthScope)
	q.Set("state", state)
	q.Set("allow_signup", "true")
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

// Exchange turns an authorization code into verified GitHub claims. redirectURI must
// match the value the browser used to obtain the code, or GitHub rejects the exchange.
func (v *GitHubVerifier) Exchange(ctx context.Context, code, redirectURI string) (GitHubClaims, error) {
	if !v.Enabled() {
		return GitHubClaims{}, errors.New("github: login disabled")
	}
	if strings.TrimSpace(code) == "" {
		return GitHubClaims{}, errors.New("github: missing code")
	}

	token, err := v.exchangeCode(ctx, code, redirectURI)
	if err != nil {
		return GitHubClaims{}, err
	}

	claims, err := v.fetchUser(ctx, token)
	if err != nil {
		return GitHubClaims{}, err
	}

	// GitHub omits the email from /user when the user keeps it private, so read the
	// dedicated emails endpoint and take the primary, verified address. An unverified
	// or absent email is left blank — never trusted for account linking.
	email, verified := v.fetchPrimaryEmail(ctx, token)
	claims.Email, claims.EmailVerified = email, verified
	return claims, nil
}

func (v *GitHubVerifier) exchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("client_id", v.clientID)
	form.Set("client_secret", v.clientSecret)
	form.Set("code", code)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", githubUserAgent)

	resp, err := v.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		AccessToken      string `json:"access_token"`
		Scope            string `json:"scope"`
		TokenType        string `json:"token_type"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("github: bad token response: %w", err)
	}
	if out.Error != "" {
		// e.g. bad_verification_code (code reused/expired) or redirect_uri_mismatch.
		return "", fmt.Errorf("github: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", errors.New("github: no access token returned")
	}
	return out.AccessToken, nil
}

func (v *GitHubVerifier) fetchUser(ctx context.Context, token string) (GitHubClaims, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubUserURL, nil)
	if err != nil {
		return GitHubClaims{}, err
	}
	v.setAuth(req, token)
	resp, err := v.client.Do(req)
	if err != nil {
		return GitHubClaims{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return GitHubClaims{}, fmt.Errorf("github: /user returned %d", resp.StatusCode)
	}
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return GitHubClaims{}, err
	}
	if u.ID == 0 {
		return GitHubClaims{}, errors.New("github: user has no id")
	}
	return GitHubClaims{
		ID:    strconv.FormatInt(u.ID, 10),
		Login: u.Login,
		Name:  u.Name,
	}, nil
}

// fetchPrimaryEmail returns the account's primary, verified email. Best-effort: on any
// error it returns ("", false) so login proceeds on the id alone rather than failing —
// the numeric id is the real identity, the email is only a convenience for linking.
func (v *GitHubVerifier) fetchPrimaryEmail(ctx context.Context, token string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubEmailsURL, nil)
	if err != nil {
		return "", false
	}
	v.setAuth(req, token)
	resp, err := v.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", false
	}
	// Prefer the primary verified address; fall back to any verified one.
	var fallback string
	for _, e := range emails {
		if !e.Verified {
			continue
		}
		if e.Primary {
			return e.Email, true
		}
		if fallback == "" {
			fallback = e.Email
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

func (v *GitHubVerifier) setAuth(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubUserAgent)
}
