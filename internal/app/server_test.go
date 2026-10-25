package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) *Server {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "atlas.db"), "https://maps.gravitas.cool", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"alice", "bob", "eve"} {
		if _, err = s.db.Exec(`INSERT INTO users VALUES(?,?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func call(t *testing.T, fn userHandler, id, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	fn(w, r, id)
	return w
}
func TestPrivateEndpointsRequireAuthenticationAndCSRF(t *testing.T) {
	s := fixture(t)
	for _, path := range []string{"/api/state", "/api/friends", "/api/me"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, origin := range []string{"https://evil.test", "https://maps.gravitas.cool"} {
		r := httptest.NewRequest("PUT", "/api/state", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("missing CSRF accepted: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/invites", nil)
	r.Header.Set("X-Gravitas-CSRF", "1")
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
}
func TestStateIsolationAndConflict(t *testing.T) {
	s := fixture(t)
	data := `{"lists":[{"id":"list-1","name":"Coffee","places":[]}],"highlights":[],"revision":0}`
	w := call(t, s.putState, "alice", "PUT", data)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(t, s.getState, "bob", "GET", "")
	if strings.Contains(w.Body.String(), "Coffee") {
		t.Fatal("another user's list leaked")
	}
	w = call(t, s.getState, "alice", "GET", "")
	if !strings.Contains(w.Body.String(), "Coffee") || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatal(w.Body.String())
	}
	w = call(t, s.putState, "alice", "PUT", data)
	if w.Code != 409 {
		t.Fatalf("concurrent overwrite not rejected: %d", w.Code)
	}
	w = call(t, s.putState, "alice", "PUT", strings.Replace(data, `"revision":0`, `"revision":1`, 1))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestInvitationConsentExpiryAndFriendIsolation(t *testing.T) {
	s := fixture(t)
	w := call(t, s.invite, "alice", "POST", "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var invite map[string]string
	json.Unmarshal(w.Body.Bytes(), &invite)
	token := strings.Split(invite["url"], "#invite=")[1]
	body := `{"token":"` + token + `"}`
	w = call(t, s.accept, "alice", "POST", body)
	if w.Code != 400 {
		t.Fatal("self invitation accepted")
	}
	w = call(t, s.accept, "bob", "POST", body)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(t, s.accept, "eve", "POST", body)
	if w.Code != 404 {
		t.Fatal("invitation reused")
	}
	w = call(t, s.getFriends, "bob", "GET", "")
	if !strings.Contains(w.Body.String(), `"sharing":false`) {
		t.Fatal("accepting started sharing")
	}
	w = call(t, s.location, "alice", "PUT", `{"lat":43.65,"lng":-79.38,"accuracy":10}`)
	if w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	w = call(t, s.getFriends, "bob", "GET", "")
	if !strings.Contains(w.Body.String(), `"lat":43.65`) {
		t.Fatal("accepted friend cannot see location")
	}
	w = call(t, s.getFriends, "eve", "GET", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "43.65") {
		t.Fatal("location exposed to stranger")
	}
	call(t, s.stopLocation, "alice", "DELETE", "")
	w = call(t, s.getFriends, "bob", "GET", "")
	if strings.Contains(w.Body.String(), `"lat"`) {
		t.Fatal("paused location still visible")
	}
	call(t, s.location, "alice", "PUT", `{"lat":43.65,"lng":-79.38,"accuracy":10}`)
	s.db.Exec(`UPDATE locations SET expires=?`, time.Now().Unix()-1)
	w = call(t, s.getFriends, "bob", "GET", "")
	if strings.Contains(w.Body.String(), `"lat"`) {
		t.Fatal("expired location still visible")
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM locations`).Scan(&count)
	if count != 0 {
		t.Fatal("expired coordinates retained")
	}
	w = call(t, s.invite, "eve", "POST", "")
	json.Unmarshal(w.Body.Bytes(), &invite)
	s.db.Exec(`UPDATE invites SET expires=?`, time.Now().Unix()-1)
	token = strings.Split(invite["url"], "#invite=")[1]
	w = call(t, s.accept, "bob", "POST", `{"token":"`+token+`"}`)
	if w.Code != 404 {
		t.Fatal("expired invite accepted")
	}
}
func TestRemoveFriendIsMutual(t *testing.T) {
	s := fixture(t)
	s.db.Exec(`INSERT INTO friends VALUES('alice','bob'),('bob','alice')`)
	r := httptest.NewRequest("DELETE", "/api/friends/bob", nil)
	r.SetPathValue("id", "bob")
	w := httptest.NewRecorder()
	s.removeFriend(w, r, "alice")
	var count int
	s.db.QueryRow(`SELECT count(*) FROM friends`).Scan(&count)
	if count != 0 {
		t.Fatal("friend removal was not mutual")
	}
}
func TestInvalidCoordinatesAndMalformedBodies(t *testing.T) {
	s := fixture(t)
	for _, body := range []string{`{"lat":91,"lng":0,"accuracy":1}`, `{"lat":0,"lng":181,"accuracy":1}`, `{"lat":0,"lng":0,"accuracy":-1}`, `{"lat":0,"lng":0,"accuracy":1,"owner":"alice"}`, `{} {}`} {
		w := call(t, s.location, "alice", "PUT", body)
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
}
func TestStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent.db")
	s, err := New(path, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`INSERT INTO users VALUES('alice','Alice')`)
	w := call(t, s.putState, "alice", "PUT", `{"lists":[{"id":"1","name":"Retained","places":[]}],"highlights":[],"revision":0}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.Close()
	s, err = New(path, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w = call(t, s.getState, "alice", "GET", "")
	if !strings.Contains(w.Body.String(), "Retained") {
		t.Fatal("state did not survive restart")
	}
}
func TestHealthAndStaticApp(t *testing.T) {
	s := fixture(t)
	for _, path := range []string{"/healthz", "/", "/app.js", "/icon.svg", "/manifest.webmanifest"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
