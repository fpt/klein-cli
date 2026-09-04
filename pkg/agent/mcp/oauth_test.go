package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// oauthServer configures a server with OAuth on, pointed at url.
func oauthServer(t *testing.T, serverURL string) domain.MCPServerConfig {
	t.Helper()
	return domain.MCPServerConfig{
		Name:    "datadog",
		Enabled: true,
		Type:    domain.MCPServerTypeHTTP,
		URL:     serverURL,
		OAuth: &domain.MCPOAuthConfig{
			Enabled:      true,
			Scopes:       []string{"mcp_all"},
			StoreDir:     t.TempDir(),
			RedirectPort: freePort(t),
		},
	}
}

// freePort reserves a port and hands it back. The login flow binds the port
// itself, so tests must not hold it — they only need one nothing else is using,
// since a fixed default would collide when tests run in parallel.
func freePort(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().(*net.TCPAddr)
	srv.Close()
	return addr.Port
}

// A missing file is "not authorized yet", not a read failure. The handler keys
// its whole retry path off ErrNoToken, so returning anything else would turn a
// first run into a hard error instead of a login prompt.
func TestCredentialStore_MissingFileReportsNoToken(t *testing.T) {
	t.Parallel()

	store := NewCredentialStore(t.TempDir(), "datadog")
	if _, err := store.GetToken(context.Background()); !errors.Is(err, transport.ErrNoToken) {
		t.Fatalf("want transport.ErrNoToken for an absent file, got %v", err)
	}
}

// The registration has to survive a token write. mcp-go hands SaveToken a token
// and nothing else, so a store that rewrote the file from that alone would drop
// the client_id and silently re-register on the next login.
func TestCredentialStore_SaveTokenKeepsClientRegistration(t *testing.T) {
	t.Parallel()

	store := NewCredentialStore(t.TempDir(), "datadog")
	if err := store.SaveClient("client-abc", "secret-xyz"); err != nil {
		t.Fatalf("SaveClient: %v", err)
	}
	if err := store.SaveToken(context.Background(), &transport.Token{AccessToken: "at"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	id, secret, err := store.Client()
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if id != "client-abc" || secret != "secret-xyz" {
		t.Errorf("the registration was lost on token save: got %q/%q", id, secret)
	}
}

// A credential file is a bearer credential; it must not be group- or
// world-readable.
func TestCredentialStore_FileIsPrivate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewCredentialStore(dir, "datadog")
	if err := store.SaveToken(context.Background(), &transport.Token{AccessToken: "at"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credential file mode = %o, want 600", perm)
	}
}

// Server names come from TOML table keys, and a quoted key can hold separators.
// The store must not let one address a file outside its directory.
func TestCredentialStore_NameCannotEscapeTheStoreDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewCredentialStore(dir, "../../etc/passwd")
	if got := filepath.Dir(store.Path()); got != dir {
		t.Errorf("a hostile server name escaped the store dir: %s", store.Path())
	}
}

// Under OAuth the handler owns the Authorization header. A static token left in
// place would be a second, stale value for the same header.
func TestBuildAuthHeaders_OAuthDropsTheStaticToken(t *testing.T) {
	t.Parallel()

	cfg := domain.MCPServerConfig{
		AuthorizationToken: "static-token",
		Headers:            map[string]string{"DD_API_KEY": "k"},
		OAuth:              &domain.MCPOAuthConfig{Enabled: true},
	}
	headers := buildAuthHeaders(cfg)

	if _, ok := headers["Authorization"]; ok {
		t.Error("the static bearer token was sent alongside OAuth")
	}
	if headers["DD_API_KEY"] != "k" {
		t.Error("non-auth headers should survive: a server can want both")
	}
}

// Without OAuth nothing changes — the static token is still the credential.
func TestBuildAuthHeaders_WithoutOAuthKeepsTheStaticToken(t *testing.T) {
	t.Parallel()

	headers := buildAuthHeaders(domain.MCPServerConfig{AuthorizationToken: "static-token"})
	if headers["Authorization"] != "Bearer static-token" {
		t.Errorf("Authorization = %q, want the static bearer token", headers["Authorization"])
	}
}

// Starting a never-logged-in server must say "log in", not fail somewhere in the
// transport with a message that reads like a rejected credential.
func TestStart_UnauthenticatedOAuthServerAsksForLogin(t *testing.T) {
	t.Parallel()

	cfg := oauthServer(t, "https://example.invalid/mcp")
	client, err := NewMCPClient(cfg)
	if err != nil {
		t.Fatalf("NewMCPClient: %v", err)
	}
	err = client.Start(context.Background())
	if !errors.Is(err, ErrOAuthLoginRequired) {
		t.Fatalf("want ErrOAuthLoginRequired, got %v", err)
	}
}

// A stored token is enough to get past the check, so a logged-in server connects
// without anyone being present.
func TestHasOAuthCredentials_TrueAfterAToken(t *testing.T) {
	t.Parallel()

	cfg := oauthServer(t, "https://example.invalid/mcp")
	if HasOAuthCredentials(context.Background(), cfg) {
		t.Fatal("reported credentials before any login")
	}

	store := NewCredentialStore(cfg.OAuth.StoreDir, cfg.Name)
	if err := store.SaveToken(context.Background(), &transport.Token{
		AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if !HasOAuthCredentials(context.Background(), cfg) {
		t.Error("a stored token should satisfy the check")
	}
}

const (
	tokenPath           = "/token"
	preregisteredClient = "preregistered"
	challengeMethodS256 = "S256"
	oauthCodeParam      = "code"
	registeredClientID  = "issued-client-id"
	stubAuthCode        = "the-auth-code"
)

// stubAuthServer stands up an authorization server: metadata discovery, plus
// whatever endpoints a test wires in. The metadata document is the same shape
// every time and only the endpoints differ, so it lives here rather than in each
// test.
func stubAuthServer(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		meta := map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/authorize",
			"token_endpoint":                        base + "/token",
			"response_types_supported":              []string{oauthCodeParam},
			"code_challenge_methods_supported":      []string{challengeMethodS256},
			"token_endpoint_auth_methods_supported": []string{"none"},
		}
		if _, ok := routes["/register"]; ok {
			meta["registration_endpoint"] = base + "/register"
		}
		writeJSON(t, w, meta)
	})
	for path, handler := range routes {
		mux.HandleFunc(path, handler)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// deliverCallback stands in for the browser: it reads what klein put in the
// authorization URL and delivers the given query to klein's loopback callback.
func deliverCallback(authURL string, query func(url.Values) url.Values) error {
	parsed, err := url.Parse(authURL)
	if err != nil {
		return fmt.Errorf("parsing the authorization URL: %w", err)
	}
	redirect, err := url.Parse(parsed.Query().Get("redirect_uri"))
	if err != nil {
		return fmt.Errorf("parsing the redirect URI: %w", err)
	}
	redirect.RawQuery = query(parsed.Query()).Encode()

	go func() {
		resp, getErr := http.Get(redirect.String())
		if getErr == nil {
			_ = resp.Body.Close()
		}
	}()
	return nil
}

// authTrace is what the stub authorization server observed.
type authTrace struct {
	challengeMethod string
	scope           string
	tokenClientID   string
	verifierLen     int
}

// runStubLogin drives a complete login against a stub server and returns the
// config it used along with what the server saw.
func runStubLogin(t *testing.T) (domain.MCPServerConfig, *authTrace) {
	t.Helper()

	trace := &authTrace{}
	authServer := stubAuthServer(t, map[string]http.HandlerFunc{
		"/register": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, map[string]any{"client_id": registeredClientID})
		},
		tokenPath: func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parsing the token request: %v", err)
			}
			trace.tokenClientID = r.Form.Get("client_id")
			trace.verifierLen = len(r.Form.Get("code_verifier"))
			writeJSON(t, w, map[string]any{
				"access_token":  "issued-access-token",
				"refresh_token": "issued-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		},
	})

	cfg := oauthServer(t, authServer.URL+"/mcp")
	opts := LoginOptions{OpenURL: func(authURL string) error {
		return deliverCallback(authURL, func(q url.Values) url.Values {
			trace.challengeMethod = q.Get("code_challenge_method")
			trace.scope = q.Get("scope")
			return url.Values{oauthCodeParam: {stubAuthCode}, "state": {q.Get("state")}}
		})
	}}

	if err := Login(context.Background(), cfg, opts); err != nil {
		t.Fatalf("Login: %v", err)
	}
	return cfg, trace
}

// What klein puts on the wire. This is what catches the pieces being assembled
// in the wrong order — registering after building the authorization URL, say,
// which would send an empty client_id.
func TestLogin_SendsTheRegisteredClientAndPKCE(t *testing.T) {
	t.Parallel()

	_, trace := runStubLogin(t)

	if trace.challengeMethod != challengeMethodS256 {
		t.Errorf("code_challenge_method = %q, want S256 (the server requires PKCE)", trace.challengeMethod)
	}
	if trace.scope != "mcp_all" {
		t.Errorf("scope = %q, want the configured scope", trace.scope)
	}
	if trace.tokenClientID != registeredClientID {
		t.Errorf("client_id = %q, want the dynamically registered id", trace.tokenClientID)
	}
	if trace.verifierLen == 0 {
		t.Error("no PKCE code_verifier reached the token endpoint")
	}
}

// What ends up on disk. The refresh token is the part that matters most: without
// it the credential dies in an hour and a scheduled run is stuck.
func TestLogin_StoresTheTokenAndRegistration(t *testing.T) {
	t.Parallel()

	cfg, _ := runStubLogin(t)
	store := NewCredentialStore(cfg.OAuth.StoreDir, cfg.Name)

	token, err := store.GetToken(context.Background())
	if err != nil {
		t.Fatalf("the token was not stored: %v", err)
	}
	if token.AccessToken != "issued-access-token" {
		t.Errorf("stored access token = %q", token.AccessToken)
	}
	if token.RefreshToken != "issued-refresh-token" {
		t.Errorf("the refresh token was not stored: %q", token.RefreshToken)
	}
	if id, _, _ := store.Client(); id != registeredClientID {
		t.Errorf("stored client id = %q, want the registered one", id)
	}
}

// A callback whose state does not match must never reach the token endpoint.
func TestLogin_RejectsAMismatchedState(t *testing.T) {
	t.Parallel()

	authServer := stubAuthServer(t, map[string]http.HandlerFunc{
		tokenPath: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("the token endpoint was reached despite a bad state")
			w.WriteHeader(http.StatusInternalServerError)
		},
	})

	cfg := oauthServer(t, authServer.URL+"/mcp")
	cfg.OAuth.ClientID = preregisteredClient // skip registration; this is about state

	opts := LoginOptions{OpenURL: func(authURL string) error {
		return deliverCallback(authURL, func(url.Values) url.Values {
			return url.Values{oauthCodeParam: {stubAuthCode}, "state": {"not-the-state-we-sent"}}
		})
	}}

	err := Login(context.Background(), cfg, opts)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want a state-mismatch error, got %v", err)
	}
}

// An authorization server that refuses should surface its reason, not a timeout.
func TestLogin_ReportsAnAuthorizationServerRefusal(t *testing.T) {
	t.Parallel()

	authServer := stubAuthServer(t, nil)
	cfg := oauthServer(t, authServer.URL+"/mcp")
	cfg.OAuth.ClientID = preregisteredClient

	opts := LoginOptions{OpenURL: func(authURL string) error {
		return deliverCallback(authURL, func(url.Values) url.Values {
			return url.Values{
				"error":             {"access_denied"},
				"error_description": {"the user said no"},
			}
		})
	}}

	err := Login(context.Background(), cfg, opts)
	if err == nil || !strings.Contains(err.Error(), "the user said no") {
		t.Fatalf("want the server's own reason, got %v", err)
	}
}

// The paste flow, end to end and with no listener involved: the server shows a
// code, the user types it back. Nothing binds the callback port here, which is
// the point — this is the path for a server that never redirects anywhere klein
// can hear, and for a browser running on another machine.
func TestLogin_PasteFlowCompletesWithoutAListener(t *testing.T) {
	t.Parallel()

	var tokenClientID string
	authServer := stubAuthServer(t, map[string]http.HandlerFunc{
		tokenPath: func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parsing the token request: %v", err)
			}
			tokenClientID = r.Form.Get("client_id")
			writeJSON(t, w, map[string]any{
				"access_token": "pasted-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		},
	})

	cfg := oauthServer(t, authServer.URL+"/mcp")
	cfg.OAuth.ClientID = preregisteredClient

	shown := false
	opts := LoginOptions{
		OpenURL:  func(string) error { shown = true; return nil },
		ReadCode: func() (string, error) { return "  one-time-code-123  ", nil },
	}
	if err := Login(context.Background(), cfg, opts); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if !shown {
		t.Error("the user was never shown the authorization URL")
	}
	if tokenClientID != preregisteredClient {
		t.Errorf("client_id = %q", tokenClientID)
	}
	store := NewCredentialStore(cfg.OAuth.StoreDir, cfg.Name)
	token, err := store.GetToken(context.Background())
	if err != nil || token.AccessToken != "pasted-access-token" {
		t.Fatalf("the pasted code did not produce a stored token: %v / %+v", err, token)
	}
}

// Which of the three shapes the user has depends on the server, so all three are
// accepted rather than making the user do the parsing.
func TestParsePastedCode_AcceptsCodeURLOrQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		pasted string
	}{
		{"a bare one-time code", "one-time-code-123"},
		{"the whole redirect URL", "http://localhost:33418/callback?code=one-time-code-123&state=st"},
		{"just the query string", "?code=one-time-code-123&state=st"},
		{"a query string without the leading ?", "code=one-time-code-123&state=st"},
		{"surrounding whitespace", "  one-time-code-123\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePastedCode(tc.pasted, "st")
			if err != nil {
				t.Fatalf("parsePastedCode(%q): %v", tc.pasted, err)
			}
			if got.code != "one-time-code-123" {
				t.Errorf("code = %q", got.code)
			}
			// The handler compares this against what it recorded, so a paste that
			// carried no state of its own still has to arrive with the expected one.
			if got.state != "st" {
				t.Errorf("state = %q, want the expected state", got.state)
			}
		})
	}
}

// A pasted value that does carry a state is still checked against it.
func TestParsePastedCode_RejectsAMismatchedState(t *testing.T) {
	t.Parallel()

	_, err := parsePastedCode("?code=c&state=somebody-elses-state", "st")
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want a state-mismatch error, got %v", err)
	}
}

// Pasting the error page's URL should report the server's reason.
func TestParsePastedCode_ReportsAPastedError(t *testing.T) {
	t.Parallel()

	_, err := parsePastedCode("?error=access_denied&error_description=nope", "st")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want the server's reason, got %v", err)
	}
}

// Empty input is a mistake worth naming, not an empty code sent to the server.
func TestParsePastedCode_RejectsEmptyInput(t *testing.T) {
	t.Parallel()

	if _, err := parsePastedCode("   \n", "st"); err == nil {
		t.Fatal("an empty paste should be refused")
	}
}

// Opening a non-web scheme from server-supplied metadata is refused.
func TestOpenBrowser_RefusesANonWebScheme(t *testing.T) {
	t.Parallel()

	if err := OpenBrowser("file:///etc/passwd"); err == nil {
		t.Fatal("a file:// authorization URL should be refused")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encoding the stub response: %v", err)
	}
}
