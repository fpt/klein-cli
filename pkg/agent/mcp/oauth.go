package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"

	"github.com/fpt/klein-cli/pkg/agent/domain"
)

// DefaultOAuthRedirectPort is the loopback port `klein mcp login` listens on.
//
// Fixed rather than ephemeral, and the same number every time: a client
// registered dynamically is bound to the redirect_uri it registered with, so a
// port that moved between logins would invalidate the stored registration and
// force a re-register on every login.
const DefaultOAuthRedirectPort = 33418

// loginTimeout bounds the wait for the browser leg. Long enough for an SSO
// detour with an MFA prompt, short enough that a login nobody completed does not
// hold the port forever.
const loginTimeout = 5 * time.Minute

// ErrOAuthLoginRequired reports that a server is configured for OAuth but has no
// usable stored credentials — the user has to run `klein mcp login <server>`.
//
// Distinct from the transport's own authorization error so callers can tell "you
// have never logged in" from "the server rejected what we sent", which need
// different things from the user.
var ErrOAuthLoginRequired = errors.New("no stored OAuth credentials; run `klein mcp login <server>`")

// credentials is the on-disk shape of one server's OAuth state.
//
// The client registration is stored with the token, not derived again on each
// run: dynamic registration mints a *new* client every time it is called, so
// re-registering on startup would leave a trail of orphaned client records on
// the authorization server and throw away the consent already granted to the
// previous one.
//
//nolint:tagliatelle // the file mirrors OAuth's own field names, and the embedded transport.Token already uses them
type credentials struct {
	Token        *transport.Token `json:"token,omitempty"`
	ClientID     string           `json:"client_id,omitempty"`
	ClientSecret string           `json:"client_secret,omitempty"`
}

// CredentialStore is a file-backed transport.TokenStore.
//
// A file rather than memory because the token has to outlive the process. With
// the in-memory store every `klein` invocation starts unauthenticated, which for
// a scheduled `klein claw` run means starting unauthenticated with no one there
// to open a browser.
type CredentialStore struct {
	path string
	mu   sync.Mutex
}

// NewCredentialStore returns the store for one server, at <dir>/<name>.json.
func NewCredentialStore(dir, serverName string) *CredentialStore {
	return &CredentialStore{path: filepath.Join(dir, credentialFileName(serverName))}
}

// credentialFileName maps a server name onto a single safe path element.
//
// Server names are TOML table keys, and a quoted key may hold anything at all —
// including separators and "..". Anything outside a conservative set becomes an
// underscore so the name can never climb out of the store directory.
func credentialFileName(serverName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, serverName)
	if safe == "" || strings.Trim(safe, ".") == "" {
		safe = "server"
	}
	return safe + ".json"
}

// Path is where the credentials are kept, for messages that tell the user which
// file to delete when they want to start over.
func (s *CredentialStore) Path() string { return s.path }

func (s *CredentialStore) read() (credentials, error) {
	var creds credentials
	data, err := os.ReadFile(s.path) //nolint:gosec // klein's own credential file, under its base dir
	if err != nil {
		if os.IsNotExist(err) {
			return credentials{}, nil
		}
		return credentials{}, fmt.Errorf("reading %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, fmt.Errorf("parsing %s: %w", s.path, err)
	}
	return creds, nil
}

func (s *CredentialStore) write(creds credentials) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(s.path), err)
	}
	//nolint:gosec // G117: persisting the client secret is what a credential store is for
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}
	// Written to a temporary file and renamed so a crash mid-write cannot leave
	// a truncated file where a valid refresh token used to be — losing that
	// costs an interactive login the user may not be present for.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}

// GetToken implements transport.TokenStore. A missing file or a file with no
// token is transport.ErrNoToken, the sentinel the handler treats as "not
// authorized yet" rather than as a failure.
func (s *CredentialStore) GetToken(ctx context.Context) (*transport.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading stored credentials: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.read()
	if err != nil {
		return nil, err
	}
	if creds.Token == nil || creds.Token.AccessToken == "" {
		return nil, transport.ErrNoToken
	}
	return creds.Token, nil
}

// SaveToken implements transport.TokenStore. It preserves the stored client
// registration: the handler only ever hands over a token, and rewriting the file
// from that alone would drop the client_id and force a re-register.
func (s *CredentialStore) SaveToken(ctx context.Context, token *transport.Token) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("storing credentials: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.read()
	if err != nil {
		return err
	}
	creds.Token = token
	return s.write(creds)
}

// Client returns the stored client registration, if any.
func (s *CredentialStore) Client() (clientID, clientSecret string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.read()
	if err != nil {
		return "", "", err
	}
	return creds.ClientID, creds.ClientSecret, nil
}

// SaveClient records a client registration, leaving any stored token alone.
func (s *CredentialStore) SaveClient(clientID, clientSecret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.read()
	if err != nil {
		return err
	}
	creds.ClientID = clientID
	creds.ClientSecret = clientSecret
	return s.write(creds)
}

// OAuthRedirectURI is the loopback callback the login flow listens on.
func OAuthRedirectURI(port int) string {
	if port == 0 {
		port = DefaultOAuthRedirectPort
	}
	return fmt.Sprintf("http://localhost:%d/callback", port)
}

// oauthConfig assembles the transport's OAuth config for a server, folding in
// whatever the store already knows about the client.
//
// A stored client id beats the configured one. Dynamic registration writes its
// result to the store, and that record is the one the authorization server has
// actually seen; preferring the settings file would silently authorize as a
// different client than the one consent was granted to.
func oauthConfig(cfg domain.MCPServerConfig) (transport.OAuthConfig, *CredentialStore, error) {
	if cfg.OAuth == nil || !cfg.OAuth.Enabled {
		return transport.OAuthConfig{}, nil, fmt.Errorf("MCP server %q does not have OAuth enabled", cfg.Name)
	}
	if cfg.OAuth.StoreDir == "" {
		return transport.OAuthConfig{}, nil, fmt.Errorf("MCP server %q: OAuth store directory was not set", cfg.Name)
	}

	store := NewCredentialStore(cfg.OAuth.StoreDir, cfg.Name)
	clientID, clientSecret, err := store.Client()
	if err != nil {
		return transport.OAuthConfig{}, nil, err
	}
	if clientID == "" {
		clientID, clientSecret = cfg.OAuth.ClientID, cfg.OAuth.ClientSecret
	}

	return transport.OAuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  OAuthRedirectURI(cfg.OAuth.RedirectPort),
		Scopes:       cfg.OAuth.Scopes,
		TokenStore:   store,
		// Always on. Every server worth pointing this at requires it (Datadog
		// advertises pkce_required: true), and a server that does not care
		// ignores the extra parameters.
		PKCEEnabled: true,
	}, store, nil
}

// HasOAuthCredentials reports whether a login has already happened — a token is
// stored, or at least a refresh token that can produce one.
//
// Used to fail a startup early and legibly instead of letting the first tool
// call surface a transport-level authorization error mid-turn.
func HasOAuthCredentials(ctx context.Context, cfg domain.MCPServerConfig) bool {
	if cfg.OAuth == nil || !cfg.OAuth.Enabled || cfg.OAuth.StoreDir == "" {
		return false
	}
	store := NewCredentialStore(cfg.OAuth.StoreDir, cfg.Name)
	_, err := store.GetToken(ctx)
	return err == nil
}

// LoginOptions carries what the flow needs from its caller.
type LoginOptions struct {
	// OpenURL shows the user the authorization URL. Injected so tests can drive
	// the flow without a browser, and so a caller can print the URL rather than
	// launch something nobody is looking at.
	OpenURL func(string) error

	// ReadCode, when set, replaces the loopback listener with a paste: klein
	// shows the URL and asks the caller for whatever the authorization server
	// ended up displaying.
	//
	// This is not only for headless machines. Plenty of authorization servers
	// never deliver a usable redirect at all — they finish on a page showing a
	// one-time code to copy — and for those the listener waits for a callback
	// that is never coming. It is also the only workable shape when the browser
	// is on a different machine than klein, since the redirect then lands on
	// *that* machine's loopback.
	ReadCode func() (string, error)
}

// Login runs the interactive authorization-code flow for one server and stores
// the resulting token.
func Login(ctx context.Context, cfg domain.MCPServerConfig, opts LoginOptions) error {
	oauthCfg, store, err := oauthConfig(cfg)
	if err != nil {
		return err
	}

	handler := transport.NewOAuthHandler(oauthCfg)
	handler.SetBaseURL(cfg.URL)

	if regErr := ensureClient(ctx, handler, store, oauthCfg.ClientID, cfg.URL); regErr != nil {
		return regErr
	}

	result, verifier, authErr := authorize(ctx, handler, cfg.OAuth.RedirectPort, opts)
	if authErr != nil {
		return authErr
	}
	if err := handler.ProcessAuthorizationResponse(ctx, result.code, result.state, verifier); err != nil {
		return fmt.Errorf("exchanging the authorization code: %w", err)
	}
	return nil
}

// parsePastedCode reads back whatever the authorization server showed the user.
//
// Three shapes are accepted, because which one the user has depends on a server
// klein does not control: the bare code, the full redirect URL the browser
// landed on, and the query string on its own. Insisting on one of them would
// make the user do the parsing.
//
// A pasted value carrying its own state is checked against expectedState. A bare
// code cannot be — there is nothing to compare — and it is accepted anyway, with
// the expected state supplied on its behalf. That is a real reduction in CSRF
// protection, and it is the trade the paste flow makes: the value reached klein
// because a person moved it there deliberately, which is a different kind of
// assurance than a redirect klein can attribute, but not nothing.
func parsePastedCode(pasted, expectedState string) (callbackResult, error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return callbackResult{}, errors.New("no authorization code was entered")
	}

	// No "=" anywhere means it cannot be a URL or a query string, so it is the
	// code itself. Checked this way rather than by looking for a scheme: the
	// query string alone is a legitimate paste and carries no scheme.
	if !strings.Contains(pasted, "=") {
		return callbackResult{code: pasted, state: expectedState}, nil
	}

	query, err := pastedQuery(pasted)
	if err != nil {
		return callbackResult{}, err
	}
	if errCode := query.Get("error"); errCode != "" {
		detail := query.Get("error_description")
		if detail == "" {
			detail = errCode
		}
		return callbackResult{}, fmt.Errorf("the authorization server refused the request: %s", detail)
	}

	code := query.Get("code")
	if code == "" {
		return callbackResult{}, errors.New(
			"the pasted value carried no authorization code; paste the code itself, or the whole URL you landed on")
	}
	if state := query.Get("state"); state != "" && state != expectedState {
		return callbackResult{}, errors.New("the pasted value carried an unexpected state value; the login was not completed")
	}
	return callbackResult{code: code, state: expectedState}, nil
}

// pastedQuery pulls the query parameters out of a full URL or a bare query
// string.
func pastedQuery(pasted string) (url.Values, error) {
	if parsed, err := url.Parse(pasted); err == nil && parsed.Scheme != "" {
		return parsed.Query(), nil
	}
	values, err := url.ParseQuery(strings.TrimPrefix(pasted, "?"))
	if err != nil {
		return nil, fmt.Errorf("the pasted value is neither a code nor a redirect URL: %w", err)
	}
	return values, nil
}

// ensureClient registers a client when none is configured, and records what the
// authorization server issued.
//
// Registration happens before the authorization request, not after: RegisterClient
// is what fixes the redirect_uri and the client_id, and an authorization request
// built first would carry an empty client_id.
func ensureClient(
	ctx context.Context, handler *transport.OAuthHandler, store *CredentialStore, clientID, serverURL string,
) error {
	if clientID != "" {
		return nil
	}
	if err := handler.RegisterClient(ctx, "klein"); err != nil {
		return fmt.Errorf("registering an OAuth client with %s: %w", serverURL, err)
	}
	if err := store.SaveClient(handler.GetClientID(), handler.GetClientSecret()); err != nil {
		return fmt.Errorf("saving the client registration: %w", err)
	}
	return nil
}

// authorize runs the browser leg, returning the code and the PKCE verifier that
// has to accompany it at the token endpoint.
//
// The listener is started before the URL is shown, not after: a fast redirect on
// a server that was still binding its port would arrive at nothing.
func authorize(
	ctx context.Context, handler *transport.OAuthHandler, port int, opts LoginOptions,
) (callbackResult, string, error) {
	verifier, err := transport.GenerateCodeVerifier()
	if err != nil {
		return callbackResult{}, "", fmt.Errorf("generating a PKCE verifier: %w", err)
	}
	state, err := transport.GenerateState()
	if err != nil {
		return callbackResult{}, "", fmt.Errorf("generating an OAuth state: %w", err)
	}

	var callback *callbackServer
	if opts.ReadCode == nil {
		if callback, err = startCallbackServer(port); err != nil {
			return callbackResult{}, "", err
		}
		defer callback.close()
	}

	authURL, err := handler.GetAuthorizationURL(ctx, state, transport.GenerateCodeChallenge(verifier))
	if err != nil {
		return callbackResult{}, "", fmt.Errorf("building the authorization URL: %w", err)
	}
	if openErr := opts.OpenURL(authURL); openErr != nil {
		return callbackResult{}, "", openErr
	}

	if opts.ReadCode != nil {
		result, readErr := readPastedCode(opts.ReadCode, state)
		return result, verifier, readErr
	}

	waitCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	result, err := callback.wait(waitCtx)
	if err != nil {
		return callbackResult{}, "", err
	}
	// The handler compares state itself, but only once the code has been
	// presented. Checking here too keeps a mismatched callback from reaching the
	// token endpoint at all.
	if result.state != state {
		return callbackResult{}, "", errors.New(
			"the callback carried an unexpected state value; the login was not completed")
	}
	return result, verifier, nil
}

// readPastedCode asks the caller for the code and interprets what comes back.
func readPastedCode(read func() (string, error), state string) (callbackResult, error) {
	pasted, err := read()
	if err != nil {
		return callbackResult{}, fmt.Errorf("reading the authorization code: %w", err)
	}
	return parsePastedCode(pasted, state)
}

// callbackResult is what the browser handed back on the loopback listener.
type callbackResult struct {
	code  string
	state string
}

type callbackServer struct {
	server *http.Server
	result chan callbackResult
	fail   chan error
	once   sync.Once
}

// startCallbackServer listens on loopback for the authorization redirect.
//
// Bound to 127.0.0.1 rather than every interface: the code in that URL is a
// bearer credential for the length of the exchange, and there is no reason for
// anything off this machine to be able to deliver — or intercept — it.
func startCallbackServer(port int) (*callbackServer, error) {
	if port == 0 {
		port = DefaultOAuthRedirectPort
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listening on the OAuth callback port %d: %w", port, err)
	}

	cb := &callbackServer{
		result: make(chan callbackResult, 1),
		fail:   make(chan error, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cb.handle)
	cb.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() { _ = cb.server.Serve(listener) }()
	return cb, nil
}

func (c *callbackServer) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		detail := q.Get("error_description")
		if detail == "" {
			detail = errCode
		}
		c.report(nil, fmt.Errorf("the authorization server refused the request: %s", detail))
		writeCallbackPage(w, "Authorization failed. You can close this tab and check klein for details.")
		return
	}
	code := q.Get("code")
	if code == "" {
		c.report(nil, errors.New("the callback carried no authorization code"))
		writeCallbackPage(w, "Authorization failed. You can close this tab and check klein for details.")
		return
	}
	c.report(&callbackResult{code: code, state: q.Get("state")}, nil)
	writeCallbackPage(w, "klein is authorized. You can close this tab.")
}

// report delivers the first outcome and ignores the rest, so a browser that
// replays the redirect cannot block on a channel nobody is reading.
func (c *callbackServer) report(result *callbackResult, err error) {
	c.once.Do(func() {
		if err != nil {
			c.fail <- err
			return
		}
		c.result <- *result
	})
}

func (c *callbackServer) wait(ctx context.Context) (callbackResult, error) {
	select {
	case res := <-c.result:
		return res, nil
	case err := <-c.fail:
		return callbackResult{}, err
	case <-ctx.Done():
		return callbackResult{}, fmt.Errorf("timed out waiting for the browser to complete the login: %w", ctx.Err())
	}
}

func (c *callbackServer) close() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.server.Shutdown(shutdownCtx)
}

func writeCallbackPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>klein</title>"+
		"<body style=\"font:16px system-ui;padding:3rem\"><p>%s</p>", message)
}

// OpenBrowser launches the system browser at rawURL.
//
// The URL is checked before it reaches a shell-less exec: it comes from the
// authorization server's own metadata, which klein did not write, and handing an
// arbitrary scheme to `open` would let that metadata name something other than a
// web page.
func OpenBrowser(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("the authorization URL could not be parsed: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("refusing to open an authorization URL with scheme %q", parsed.Scheme)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening a browser: %w", err)
	}
	return nil
}
