package app

import (
	"atlas/internal/gravitas"
	"atlas/web"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	db     *sql.DB
	origin string
	auth   *gravitas.Client
	geo    *geoService
}
type identityKey struct{}

func New(path, origin string, auth *gravitas.Client) (*Server, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS states (owner TEXT PRIMARY KEY REFERENCES users(id), data TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS invites (hash TEXT PRIMARY KEY, owner TEXT NOT NULL REFERENCES users(id), expires INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS friends (owner TEXT NOT NULL REFERENCES users(id), friend TEXT NOT NULL REFERENCES users(id), PRIMARY KEY(owner,friend))`,
		`CREATE TABLE IF NOT EXISTS locations (owner TEXT PRIMARY KEY REFERENCES users(id), lat REAL NOT NULL, lng REAL NOT NULL, accuracy REAL NOT NULL, updated INTEGER NOT NULL, expires INTEGER NOT NULL)`,
	} {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Server{db: db, origin: origin, auth: auth, geo: newGeo()}, nil
}
func (s *Server) Close() error { return s.db.Close() }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, message string) {
	reply(w, code, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil || dec.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "Invalid request")
		return false
	}
	return true
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.db.PingContext(r.Context()) != nil {
			fail(w, 503, "Database unavailable")
			return
		}
		reply(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"signIn": s.auth != nil, "tileURL": "https://tile.openstreetmap.org/{z}/{x}/{y}.png"})
	})
	mux.HandleFunc("GET /api/search", s.geo.search)
	mux.HandleFunc("GET /api/places", s.geo.places)
	mux.HandleFunc("GET /api/route", s.geo.route)
	mux.HandleFunc("GET /api/streets", s.geo.streets)
	mux.HandleFunc("GET /auth/gravitas/start", func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			fail(w, 503, "Sign-in is not configured")
			return
		}
		if s.auth.Start(w, r, false) != nil {
			fail(w, 503, "Gravitas sign-in is temporarily unavailable")
		}
	})
	mux.HandleFunc("GET /auth/gravitas/callback", func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			fail(w, 503, "Sign-in is not configured")
			return
		}
		id, next, err := s.auth.Callback(w, r)
		if err != nil {
			http.Redirect(w, r, "/?login=failed", 303)
			return
		}
		if err = s.auth.SetSession(w, id); err != nil {
			fail(w, 503, "Unable to sign in")
			return
		}
		http.Redirect(w, r, gravitas.SafeNext(next), 303)
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil || !s.auth.ValidMutation(r) {
			fail(w, 403, "Invalid logout")
			return
		}
		s.auth.ClearSession(w)
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/me", s.protected(s.me))
	mux.HandleFunc("GET /api/state", s.protected(s.getState))
	mux.HandleFunc("PUT /api/state", s.protected(s.putState))
	mux.HandleFunc("GET /api/friends", s.protected(s.getFriends))
	mux.HandleFunc("POST /api/invites", s.protected(s.invite))
	mux.HandleFunc("POST /api/invites/accept", s.protected(s.accept))
	mux.HandleFunc("DELETE /api/friends/{id}", s.protected(s.removeFriend))
	mux.HandleFunc("PUT /api/location", s.protected(s.location))
	mux.HandleFunc("DELETE /api/location", s.protected(s.stopLocation))
	mux.Handle("GET /", http.FileServer(http.FS(web.Files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: https://tile.openstreetmap.org; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=()")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && (r.Header.Get("X-Gravitas-CSRF") != "1" || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.origin)) {
			fail(w, 403, "Invalid request origin")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type userHandler func(http.ResponseWriter, *http.Request, string)

func (s *Server) protected(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			fail(w, 401, "Sign in with Gravitas to sync and share")
			return
		}
		id, err := s.auth.Session(r)
		if err != nil {
			if errors.Is(err, gravitas.ErrUnavailable) {
				fail(w, 503, "Gravitas is temporarily unavailable")
			} else {
				fail(w, 401, "Sign in with Gravitas")
			}
			return
		}
		owner := id.Issuer + "|" + id.Subject
		if _, err = s.db.ExecContext(r.Context(), `INSERT INTO users(id,name) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name`, owner, id.Username); err != nil {
			fail(w, 500, "Unable to load account")
			return
		}
		next(w, r, owner)
	}
}
func (s *Server) me(w http.ResponseWriter, r *http.Request, id string) {
	var name string
	_ = s.db.QueryRow(`SELECT name FROM users WHERE id=?`, id).Scan(&name)
	reply(w, 200, map[string]string{"name": name})
}

type mapState struct {
	Lists      []json.RawMessage `json:"lists"`
	Highlights []json.RawMessage `json:"highlights"`
	Revision   int               `json:"revision"`
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request, id string) {
	var data string
	var rev int
	err := s.db.QueryRow(`SELECT data,revision FROM states WHERE owner=?`, id).Scan(&data, &rev)
	if err == sql.ErrNoRows {
		reply(w, 200, mapState{Lists: []json.RawMessage{}, Highlights: []json.RawMessage{}})
		return
	}
	if err != nil {
		fail(w, 500, "Unable to load saved map")
		return
	}
	var state mapState
	if json.Unmarshal([]byte(data), &state) != nil {
		fail(w, 500, "Unable to read saved map")
		return
	}
	state.Revision = rev
	reply(w, 200, state)
}
func (s *Server) putState(w http.ResponseWriter, r *http.Request, id string) {
	var state mapState
	if !decode(w, r, &state) {
		return
	}
	if len(state.Lists) > 100 || len(state.Highlights) > 200 || state.Revision < 0 {
		fail(w, 400, "Too many lists or highlights")
		return
	}
	data, _ := json.Marshal(state)
	var result sql.Result
	var err error
	if state.Revision == 0 {
		result, err = s.db.Exec(`INSERT INTO states(owner,data,revision) VALUES(?,?,1) ON CONFLICT(owner) DO NOTHING`, id, string(data))
	} else {
		result, err = s.db.Exec(`UPDATE states SET data=?, revision=revision+1 WHERE owner=? AND revision=?`, string(data), id, state.Revision)
	}
	if err != nil {
		fail(w, 500, "Unable to save map")
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		fail(w, 409, "Your map changed on another device. Reload to sync.")
		return
	}
	reply(w, 200, map[string]int{"revision": state.Revision + 1})
}
func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }
func (s *Server) invite(w http.ResponseWriter, r *http.Request, id string) {
	t := token()
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, 500, "Unable to invite")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(`DELETE FROM invites WHERE owner=? OR expires<=?`, id, time.Now().Unix())
	if err == nil {
		_, err = tx.Exec(`INSERT INTO invites VALUES(?,?,?)`, hash(t), id, time.Now().Add(24*time.Hour).Unix())
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "Unable to invite")
		return
	}
	reply(w, 201, map[string]string{"url": s.origin + "/#invite=" + t})
}
func (s *Server) accept(w http.ResponseWriter, r *http.Request, id string) {
	var v struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &v) {
		return
	}
	if len(v.Token) != 43 {
		fail(w, 400, "Invalid invitation")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, 500, "Unable to accept")
		return
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRow(`SELECT owner FROM invites WHERE hash=? AND expires>?`, hash(v.Token), time.Now().Unix()).Scan(&owner)
	if err != nil {
		fail(w, 404, "Invitation expired or already accepted")
		return
	}
	if owner == id {
		fail(w, 400, "Send this invitation to a friend")
		return
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO friends VALUES(?,?),(?,?)`, id, owner, owner, id); err != nil {
		fail(w, 500, "Unable to connect")
		return
	}
	if _, err = tx.Exec(`DELETE FROM invites WHERE hash=?`, hash(v.Token)); err != nil || tx.Commit() != nil {
		fail(w, 500, "Unable to accept")
		return
	}
	reply(w, 200, map[string]bool{"accepted": true})
}
func (s *Server) getFriends(w http.ResponseWriter, r *http.Request, id string) {
	// Expired coordinates are actively removed, including after an unclean client exit.
	if _, err := s.db.Exec(`DELETE FROM locations WHERE expires<=?`, time.Now().Unix()); err != nil {
		fail(w, 500, "Unable to load friends")
		return
	}
	rows, err := s.db.Query(`SELECT u.id,u.name,l.lat,l.lng,l.accuracy,l.updated FROM friends f JOIN users u ON u.id=f.friend LEFT JOIN locations l ON l.owner=u.id AND l.expires>? WHERE f.owner=? ORDER BY u.name`, time.Now().Unix(), id)
	if err != nil {
		fail(w, 500, "Unable to load friends")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var uid, name string
		var lat, lng, accuracy sql.NullFloat64
		var updated sql.NullInt64
		if rows.Scan(&uid, &name, &lat, &lng, &accuracy, &updated) != nil {
			fail(w, 500, "Unable to read friends")
			return
		}
		f := map[string]any{"id": uid, "name": name, "sharing": lat.Valid}
		if lat.Valid {
			f["lat"] = lat.Float64
			f["lng"] = lng.Float64
			f["accuracy"] = accuracy.Float64
			f["updated"] = updated.Int64
		}
		out = append(out, f)
	}
	if rows.Err() != nil {
		fail(w, 500, "Unable to read friends")
		return
	}
	reply(w, 200, out)
}
func (s *Server) removeFriend(w http.ResponseWriter, r *http.Request, id string) {
	other := r.PathValue("id")
	_, err := s.db.Exec(`DELETE FROM friends WHERE (owner=? AND friend=?) OR (owner=? AND friend=?)`, id, other, other, id)
	if err != nil {
		fail(w, 500, "Unable to remove friend")
		return
	}
	w.WriteHeader(204)
}
func validCoord(lat, lng float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lng) && !math.IsInf(lat, 0) && !math.IsInf(lng, 0) && lat >= -85 && lat <= 85 && lng >= -180 && lng <= 180
}
func (s *Server) location(w http.ResponseWriter, r *http.Request, id string) {
	var v struct {
		Lat      float64 `json:"lat"`
		Lng      float64 `json:"lng"`
		Accuracy float64 `json:"accuracy"`
	}
	if !decode(w, r, &v) {
		return
	}
	if !validCoord(v.Lat, v.Lng) || math.IsNaN(v.Accuracy) || v.Accuracy < 0 || v.Accuracy > 100000 {
		fail(w, 400, "Invalid location")
		return
	}
	_, err := s.db.Exec(`INSERT INTO locations VALUES(?,?,?,?,?,?) ON CONFLICT(owner) DO UPDATE SET lat=excluded.lat,lng=excluded.lng,accuracy=excluded.accuracy,updated=excluded.updated,expires=excluded.expires`, id, v.Lat, v.Lng, v.Accuracy, time.Now().Unix(), time.Now().Add(2*time.Minute).Unix())
	if err != nil {
		fail(w, 500, "Unable to share location")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) stopLocation(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := s.db.Exec(`DELETE FROM locations WHERE owner=?`, id); err != nil {
		fail(w, 500, "Unable to pause sharing")
		return
	}
	w.WriteHeader(204)
}
