package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testJWT(issuer, subject string, expiry int64, role, audience string) string {
	body, _ := json.Marshal(map[string]any{"iss": issuer, "sub": subject, "exp": expiry, "role": role, "aud": audience})
	return "test." + base64.RawURLEncoding.EncodeToString(body) + ".signature-verified-by-auth-service"
}

func TestSupabaseUsesVerifiedUserNotUntrustedClaims(t *testing.T) {
	calls := 0
	response := `{"id":"owner","is_anonymous":false}`
	status := http.StatusOK
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/auth/v1/user" || r.Header.Get("apikey") != "public-key" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") { t.Error("wrong Auth request") }
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	defer service.Close()
	authenticate, err := supabaseAuthenticatorWithClient(service.URL, "public-key", service.Client())
	if err != nil { t.Fatal(err) }
	valid := testJWT(service.URL+"/auth/v1", "owner", time.Now().Add(time.Hour).Unix(), "authenticated", "authenticated")
	identity, err := authenticate(context.Background(), valid)
	if err != nil || identity.ID != "owner" { t.Fatalf("valid account rejected: %v", err) }
	for _, token := range []string{
		"not-a-jwt", testJWT("https://another-project/auth/v1", "owner", time.Now().Add(time.Hour).Unix(), "authenticated", "authenticated"),
		testJWT(service.URL+"/auth/v1", "owner", time.Now().Add(-time.Second).Unix(), "authenticated", "authenticated"),
		testJWT(service.URL+"/auth/v1", "owner", time.Now().Add(time.Hour).Unix(), "service_role", "authenticated"),
		testJWT(service.URL+"/auth/v1", "owner", time.Now().Add(time.Hour).Unix(), "authenticated", "other"),
	} { if _, err := authenticate(context.Background(), token); err == nil { t.Fatal("invalid claim set accepted") } }
	if calls != 1 { t.Fatalf("invalid claims reached Auth: calls=%d", calls) }
	for _, body := range []string{`{"id":"different-owner"}`, `{"id":"owner","is_anonymous":true}`, `{"id":"owner","banned_until":"2999-01-01T00:00:00Z"}`, `{`, strings.Repeat("x", 65537)} {
		response = body
		if _, err := authenticate(context.Background(), valid); err == nil { t.Fatal("unverified/anonymous account accepted") }
	}
	response = `{"id":"owner"}`
	status = http.StatusUnauthorized
	if _, err := authenticate(context.Background(), valid); err == nil { t.Fatal("forged or invalid signature accepted") }
	status = http.StatusTemporaryRedirect
	if _, err := authenticate(context.Background(), valid); err == nil { t.Fatal("redirect accepted") }
}

func TestWebSocketFailsClosedWithoutAuthentication(t *testing.T) {
	s := testServer()
	for _, authorization := range []string{"", "Bearer anything"} {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set("Authorization", authorization)
		w := httptest.NewRecorder()
		s.handleWebSocket(w, r)
		if w.Code != http.StatusServiceUnavailable { t.Fatal("unconfigured server accepted a connection") }
	}
	s.authenticate = func(context.Context, string) (accountIdentity, error) { return accountIdentity{ID: "owner", ExpiresAt: time.Now().Add(-time.Second)}, nil }
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer expired")
	s.handleWebSocket(w, r)
	if w.Code != http.StatusUnauthorized { t.Fatal("expired account accepted") }
}

func TestRoomPasswordsCannotBeBypassedByAutoJoin(t *testing.T) {
	s := testServer()
	host := newClient("host", nil)
	host.accountID = "host-account"
	s.handleCreateRoom(host, encodeTestPayload(t, MsgTypeCreateRoom, &CreateRoomPayload{Username: "Host", Password: "correct horse", AutoJoin: true}))
	room := host.currentRoom()
	if room == nil || room.Password == nil { t.Fatal("password not set") }
	if strings.Contains(string(room.Password.Hash), "correct horse") { t.Fatal("plaintext password retained") }
	guest := newClient("guest", nil)
	guest.accountID = "guest-account"
	join := func(password string) { s.handleJoinRoom(guest, encodeTestPayload(t, MsgTypeJoinRoom, &JoinRoomPayload{RoomCode: room.Code, Username: "Guest", Password: password})) }
	join("")
	receiveRoomError(t, guest, "password_required")
	join("wrong password")
	receiveRoomError(t, guest, "password_incorrect")
	if guest.currentRoom() != nil || guest.currentPendingRoom() != nil { t.Fatal("password failure created membership") }
	join("correct horse")
	if guest.currentRoom() != room || room.Clients["guest"] != guest { t.Fatal("valid password did not join") }
	encoded, err := json.Marshal(room.State)
	if err != nil || strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "correct horse") { t.Fatal("private password leaked into room state") }
}

func TestReconnectRequiresSameAccountAndDoesNotConsumeOthersToken(t *testing.T) {
	s := testServer()
	host := newClient("host", nil)
	host.accountID = "owner"
	s.handleCreateRoom(host, encodeTestPayload(t, MsgTypeCreateRoom, &CreateRoomPayload{Username: "Host"}))
	token := host.session()
	s.handleClientDisconnect(host)
	thief := newClient("thief", nil)
	thief.accountID = "another-account"
	s.handleReconnect(thief, encodeTestPayload(t, MsgTypeReconnect, &ReconnectPayload{SessionToken: token}))
	receiveRoomError(t, thief, "session_not_found")
	if thief.currentRoom() != nil || s.sessions[token] == nil { t.Fatal("cross-account reconnect consumed or acquired session") }
	owner := newClient("returning-owner", nil)
	owner.accountID = "owner"
	s.handleReconnect(owner, encodeTestPayload(t, MsgTypeReconnect, &ReconnectPayload{SessionToken: token}))
	if owner.currentRoom() == nil || owner.currentRoom().Host != owner { t.Fatal("real owner could not reconnect") }
}

func TestAccountJoinBudgetSurvivesChangingConnections(t *testing.T) {
	s := testServer()
	for i := 0; i < 16; i++ {
		c := newClient("new-socket", nil)
		c.accountID = "same-account"
		s.handleJoinRoom(c, encodeTestPayload(t, MsgTypeJoinRoom, &JoinRoomPayload{RoomCode: "NOEXISTS", Username: "Guest"}))
		want := "room_not_found"
		if i == 15 { want = "rate_limited" }
		receiveRoomError(t, c, want)
	}
}
