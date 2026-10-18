package server

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type accountIdentity struct {
	ID string
	ExpiresAt time.Time
}

type authenticateAccount func(context.Context, string) (accountIdentity, error)

// The public project key identifies the Auth service. Only Auth's verified user
// response grants access; decoding a JWT or trusting its metadata does not.
func supabaseAuthenticator(baseURL, publicKey string) (authenticateAccount, error) {
	return supabaseAuthenticatorWithClient(baseURL, publicKey, &http.Client{Timeout: 5 * time.Second})
}

func supabaseAuthenticatorWithClient(baseURL, publicKey string, client *http.Client) (authenticateAccount, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.TrimSpace(publicKey) == "" {
		return nil, errors.New("SUPABASE_URL must be an HTTPS origin and SUPABASE_ANON_KEY must be set")
	}
	// Redirects must never forward an account credential to another origin.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpoint := strings.TrimSuffix(baseURL, "/") + "/auth/v1/user"
	return func(ctx context.Context, token string) (accountIdentity, error) {
		denied := errors.New("sign in to Echo to connect")
		if len(token) < 20 || len(token) > 8192 { return accountIdentity{}, denied }
		parts := strings.Split(token, ".")
		if len(parts) != 3 { return accountIdentity{}, denied }
		body, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil { return accountIdentity{}, denied }
		var claims struct { Subject string `json:"sub"`; Issuer string `json:"iss"`; Expiry int64 `json:"exp"`; Role string `json:"role"`; Audience json.RawMessage `json:"aud"` }
		if json.Unmarshal(body, &claims) != nil || claims.Subject == "" || claims.Issuer != strings.TrimSuffix(baseURL, "/") + "/auth/v1" || claims.Role != "authenticated" || claims.Expiry <= time.Now().Unix() {
			return accountIdentity{}, denied
		}
		var audience string
		validAudience := json.Unmarshal(claims.Audience, &audience) == nil && audience == "authenticated"
		if !validAudience {
			var audiences []string
			if json.Unmarshal(claims.Audience, &audiences) == nil { for _, a := range audiences { if a == "authenticated" { validAudience = true } } }
		}
		if !validAudience { return accountIdentity{}, denied }
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil { return accountIdentity{}, denied }
		req.Header.Set("apikey", publicKey)
		req.Header.Set("Authorization", "Bearer " + token)
		response, err := client.Do(req)
		if err != nil { return accountIdentity{}, denied }
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK { return accountIdentity{}, denied }
		data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		var user struct { ID string `json:"id"`; Anonymous bool `json:"is_anonymous"`; BannedUntil string `json:"banned_until"` }
		if err != nil || len(data) > 65536 || json.Unmarshal(data, &user) != nil || user.ID != claims.Subject || user.Anonymous { return accountIdentity{}, denied }
		if banned, err := time.Parse(time.RFC3339, user.BannedUntil); err == nil && banned.After(time.Now()) { return accountIdentity{}, denied }
		return accountIdentity{ID: user.ID, ExpiresAt: time.Unix(claims.Expiry, 0)}, nil
	}, nil
}

type rateWindow struct { start time.Time; count int }
type requestLimiter struct { mu sync.Mutex; windows map[string]rateWindow }

// Bounded and shared by all sockets: reconnecting does not reset an account's
// join/password budget. Unknown keys are denied at capacity rather than evicted.
func (l *requestLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.windows == nil { l.windows = make(map[string]rateWindow) }
	if len(l.windows) >= 10000 {
		for k, v := range l.windows { if now.Sub(v.start) >= 10*time.Minute { delete(l.windows, k) } }
		if _, ok := l.windows[key]; !ok && len(l.windows) >= 10000 { return false }
	}
	v := l.windows[key]
	if now.Sub(v.start) >= window { v = rateWindow{start: now} }
	if v.count >= limit { return false }
	v.count++
	l.windows[key] = v
	return true
}

const passwordIterations = 600000
type roomPassword struct { Salt []byte `json:"salt"`; Hash []byte `json:"hash"` }

func newRoomPassword(password string) (*roomPassword, error) {
	if password == "" { return nil, nil }
	if len(password) < 6 || len(password) > 128 { return nil, errors.New("use a password between 6 and 128 bytes") }
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil { return nil, err }
	hash, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil { return nil, err }
	return &roomPassword{Salt: salt, Hash: hash}, nil
}

func (p *roomPassword) matches(password string) bool {
	if p == nil { return true }
	if len(password) < 6 || len(password) > 128 || len(p.Salt) != 16 || len(p.Hash) != 32 { return false }
	hash, err := pbkdf2.Key(sha256.New, password, p.Salt, passwordIterations, 32)
	return err == nil && subtle.ConstantTimeCompare(hash, p.Hash) == 1
}

func (s *Server) authorizeUpgrade(w http.ResponseWriter, r *http.Request) (accountIdentity, string, bool) {
	if s.authenticate == nil { http.Error(w, "authentication is not configured", http.StatusServiceUnavailable); return accountIdentity{}, "", false }
	if !s.authLimiter.allow("global", 60, time.Second) { http.Error(w, "try again shortly", http.StatusTooManyRequests); return accountIdentity{}, "", false }
	select { case s.authSlots <- struct{}{}: default: http.Error(w, "try again shortly", http.StatusServiceUnavailable); return accountIdentity{}, "", false }
	defer func() { <-s.authSlots }()
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") { http.Error(w, "sign in to Echo", http.StatusUnauthorized); return accountIdentity{}, "", false }
	token := strings.TrimPrefix(authorization, "Bearer ")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	identity, err := s.authenticate(ctx, token)
	if err != nil || identity.ID == "" || !identity.ExpiresAt.After(time.Now()) { http.Error(w, "sign in to Echo", http.StatusUnauthorized); return accountIdentity{}, "", false }
	return identity, token, true
}

// Bound connection lifetime and recheck revoked/banned accounts. No token is
// persisted, logged, put in a URL or sent to other room members.
func (s *Server) watchAccount(c *Client, token string, expiresAt time.Time) {
	deadline := time.NewTimer(time.Until(expiresAt))
	check := time.NewTicker(time.Minute)
	defer deadline.Stop()
	defer check.Stop()
	for {
		select {
		case <-deadline.C: _ = c.Conn.Close(); return
		case <-check.C:
			if c.isClosed() { return }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			identity, err := s.authenticate(ctx, token)
			cancel()
			if err != nil || identity.ID != c.accountID { _ = c.Conn.Close(); return }
		}
	}
}

func configuredAuthenticator() (authenticateAccount, error) {
	return supabaseAuthenticator(strings.TrimSpace(os.Getenv("SUPABASE_URL")), strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY")))
}
