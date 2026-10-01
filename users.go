package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure Go: the builds need no C compiler
)

// User is an account of the web interface and the Jellyfin API. An
// administrator may download and organize; everyone else only watches.
type User struct {
	ID    string `json:"id"` // 32 hex digits
	Name  string `json:"name"`
	Hash  string `json:"hash"`
	Admin bool   `json:"admin"`
	Guest bool   `json:"-"` // not an account: a visitor of the web interface who has not signed in
}

// Progress is what a user did with a video, a series or a movie: how far
// they watched it, and what they think of it. (The JSON names are those of
// the server.json of earlier versions, which is imported once.)
type Progress struct {
	Position   float64   `json:"position"` // seconds
	Played     bool      `json:"played"`
	PlayCount  int       `json:"play_count,omitempty"`
	Favorite   bool      `json:"favorite,omitempty"`
	LastPlayed time.Time `json:"last_played"`
	Rating     int       `json:"rating,omitempty"`  // the user's own, 1–10 (half stars of five); 0: none
	Planned    time.Time `json:"planned,omitempty"` // put on the watchlist then; zero: not on it
	Note       string    `json:"note,omitempty"`    // the user's own note
}

// Viewing is what a history entry keeps of the video, so that it still
// reads well after the file was renamed or deleted.
type Viewing struct {
	ItemID    string `json:"itemId"`
	ShowID    string `json:"showId,omitempty"`
	Kind      string `json:"kind"` // kindMovie or kindEpisode
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	ShowTitle string `json:"showTitle,omitempty"`
	Season    int    `json:"season,omitempty"`
	Episode   int    `json:"episode,omitempty"`
}

// HistoryEntry is one sitting in front of a video.
type HistoryEntry struct {
	ID int64 `json:"id"`
	Viewing
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	Watched  float64   `json:"watched"`  // seconds actually played: no pauses, no skipping ahead
	Position float64   `json:"position"` // where it ended
	Duration float64   `json:"duration"`
	Finished bool      `json:"finished"`
}

type session struct {
	UserID  string    `json:"user"`
	Device  string    `json:"device,omitempty"`
	Created time.Time `json:"created"`
}

// Auth keeps the accounts, their login tokens and everything users do with
// the titles in a SQLite database (mediakeeper.db, next to the settings).
// Accounts, tokens and watch states are few and kept in memory as well;
// every change is written through at once. The history of viewings is in
// the database only.
type Auth struct {
	db       atomic.Pointer[sql.DB] // swapped when the database moves (MoveTo)
	path     atomic.Pointer[string]
	ServerID string

	mu       sync.Mutex
	users    []*User
	sessions map[string]*session             // by token
	watched  map[string]map[string]*Progress // user -> video, series or movie
}

const authSchema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users (
	id      TEXT PRIMARY KEY,
	name    TEXT NOT NULL UNIQUE COLLATE NOCASE,
	hash    TEXT NOT NULL,
	admin   INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
	token   TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device  TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS watch (
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	item_id     TEXT NOT NULL,
	position    REAL NOT NULL DEFAULT 0,
	played      INTEGER NOT NULL DEFAULT 0,
	play_count  INTEGER NOT NULL DEFAULT 0,
	favorite    INTEGER NOT NULL DEFAULT 0,
	last_played INTEGER NOT NULL DEFAULT 0,
	rating      INTEGER NOT NULL DEFAULT 0,
	planned     INTEGER NOT NULL DEFAULT 0,
	note        TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, item_id)
);
CREATE TABLE IF NOT EXISTS history (
	id         INTEGER PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	item_id    TEXT NOT NULL,
	show_id    TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL,
	title      TEXT NOT NULL,
	year       INTEGER NOT NULL DEFAULT 0,
	show_title TEXT NOT NULL DEFAULT '',
	season     INTEGER NOT NULL DEFAULT 0,
	episode    INTEGER NOT NULL DEFAULT 0,
	started    INTEGER NOT NULL,
	ended      INTEGER NOT NULL,
	watched    REAL NOT NULL DEFAULT 0,
	position   REAL NOT NULL DEFAULT 0,
	duration   REAL NOT NULL DEFAULT 0,
	finished   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS history_by_user ON history (user_id, ended DESC);
CREATE INDEX IF NOT EXISTS history_by_item ON history (user_id, item_id, ended DESC);
`

// A viewing continues the history entry of the same video if it resumes
// within this time; otherwise it is a new sitting.
const historySitting = 30 * time.Minute

// History lists only sittings of at least this much, or finished ones: a
// video opened and closed again is no viewing.
const historyMinimum = 60

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// A variable only so that tests can lower it.
var pbkdf2Rounds = 210_000

func hashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, 32)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Rounds,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func checkPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	rounds, _ := strconv.Atoi(parts[1])
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || rounds < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, rounds, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func unixTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// OpenAuth opens the database, creating it on first use. The accounts and
// watch states of an earlier version's server.json (legacy) are imported
// then, and the file is kept as server.json.old.
func OpenAuth(path, legacy string) (*Auth, error) {
	if err := writableFor(path); err != nil {
		return nil, err
	}
	fresh := !exists(path)
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if fresh {
		os.Chmod(path, 0o600) // password hashes and tokens
	}
	a := &Auth{sessions: map[string]*session{}, watched: map[string]map[string]*Progress{}}
	a.db.Store(db)
	a.path.Store(&path)
	if fresh && legacy != "" && exists(legacy) {
		if err := a.importJSON(legacy); err != nil {
			db.Close()
			os.Remove(path)
			return nil, fmt.Errorf("importing %s: %w", legacy, err)
		}
		os.Rename(legacy, legacy+".old")
	}
	if err := a.load(); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

func openDB(path string) (*sql.DB, error) {
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; the work is small
	if _, err := db.Exec(authSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

func (a *Auth) conn() *sql.DB { return a.db.Load() }

func (a *Auth) Close() error { return a.conn().Close() }

// Path is the database file.
func (a *Auth) Path() string { return *a.path.Load() }

// canMoveTo checks a new place for the database before anything changes.
func canMoveTo(path string) error {
	if exists(path) {
		return fmt.Errorf("there is a file at %s already: choose another place, or move that file away first", path)
	}
	return writableFor(path)
}

// MoveTo moves the database to another file while the server runs. SQLite
// copies it (VACUUM INTO: consistent even while it is written) over the
// only connection, so nothing is written meanwhile; then the new file is
// used and the old one is kept as <old>.old.
func (a *Auth) MoveTo(path string) error {
	if err := canMoveTo(path); err != nil {
		return err
	}
	old, oldPath := a.conn(), a.Path()
	ctx := context.Background()
	c, err := old.Conn(ctx)
	if err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		c.Close()
		os.Remove(path)
		return fmt.Errorf("cannot copy the database to %s: %w", path, err)
	}
	db, err := openDB(path)
	if err != nil {
		c.Close()
		os.Remove(path)
		return err
	}
	os.Chmod(path, 0o600)
	a.db.Store(db)
	a.path.Store(&path)
	c.Close()
	old.Close()
	os.Rename(oldPath, oldPath+".old")
	return nil
}

// writableFor checks that the database can be made and written where it
// is to be: SQLite needs to write the folder too (its -wal and -shm files
// go next to the database), and when it cannot it says only "unable to open
// database file: out of memory (14)", which sends people looking for the
// wrong thing.
func writableFor(path string) error {
	dir := filepath.Dir(path)
	who := ""
	if uid := os.Getuid(); uid >= 0 {
		who = fmt.Sprintf(" (uid %d, gid %d)", uid, os.Getgid())
	}
	problem := func(err error) error {
		return fmt.Errorf("cannot keep the database %s: %v. The folder %s must be writable by the user the server runs as%s"+
			" — with Docker, the owner of the mounted folder: chown -R %d:%d on it", path, err, dir, who, max(os.Getuid(), 0), max(os.Getgid(), 0))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return problem(err)
	}
	probe, err := os.CreateTemp(dir, ".mediakeeper-write-test-*")
	if err != nil {
		return problem(err)
	}
	probe.Close()
	os.Remove(probe.Name())
	if exists(path) {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return problem(err)
		}
		f.Close()
	}
	return nil
}

// load reads the accounts, tokens and watch states into memory.
func (a *Auth) load() error {
	db := a.conn()
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'server_id'`).Scan(&a.ServerID); errors.Is(err, sql.ErrNoRows) {
		a.ServerID = randomHex(16)
		if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('server_id', ?)`, a.ServerID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT id, name, hash, admin FROM users ORDER BY created, name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Name, &u.Hash, &u.Admin); err != nil {
			rows.Close()
			return err
		}
		a.users = append(a.users, u)
	}
	rows.Close()
	rows, err = db.Query(`SELECT token, user_id, device, created FROM sessions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var token string
		var created int64
		s := &session{}
		if err := rows.Scan(&token, &s.UserID, &s.Device, &created); err != nil {
			rows.Close()
			return err
		}
		s.Created = fromUnix(created)
		a.sessions[token] = s
	}
	rows.Close()
	return a.loadWatched()
}

func (a *Auth) loadWatched() error {
	rows, err := a.conn().Query(`SELECT user_id, item_id, position, played, play_count, favorite, last_played, rating, planned, note FROM watch`)
	if err != nil {
		return err
	}
	defer rows.Close()
	watched := map[string]map[string]*Progress{}
	for rows.Next() {
		var user, item string
		var last, planned int64
		p := &Progress{}
		if err := rows.Scan(&user, &item, &p.Position, &p.Played, &p.PlayCount, &p.Favorite, &last, &p.Rating, &planned, &p.Note); err != nil {
			return err
		}
		p.LastPlayed, p.Planned = fromUnix(last), fromUnix(planned)
		if watched[user] == nil {
			watched[user] = map[string]*Progress{}
		}
		watched[user][item] = p
	}
	a.watched = watched
	return rows.Err()
}

// importJSON copies server.json of an earlier version into the database.
func (a *Auth) importJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var old struct {
		ServerID string                          `json:"server_id"`
		Users    []*User                         `json:"users"`
		Sessions map[string]*session             `json:"sessions"`
		Watched  map[string]map[string]*Progress `json:"watched"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return err
	}
	tx, err := a.conn().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	known := map[string]bool{}
	if old.ServerID != "" { // Jellyfin apps know the server by it
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('server_id', ?)`, old.ServerID); err != nil {
			return err
		}
	}
	for i, u := range old.Users {
		if _, err := tx.Exec(`INSERT INTO users (id, name, hash, admin, created) VALUES (?, ?, ?, ?, ?)`, u.ID, u.Name, u.Hash, u.Admin, i); err != nil {
			return err
		}
		known[u.ID] = true
	}
	for token, s := range old.Sessions {
		if known[s.UserID] {
			if _, err := tx.Exec(`INSERT INTO sessions (token, user_id, device, created) VALUES (?, ?, ?, ?)`, token, s.UserID, s.Device, unixTime(s.Created)); err != nil {
				return err
			}
		}
	}
	for user, items := range old.Watched {
		if !known[user] {
			continue
		}
		for item, p := range items {
			if err := writeProgress(tx, user, item, p); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func writeProgress(db execer, user, item string, p *Progress) error {
	_, err := db.Exec(`INSERT INTO watch (user_id, item_id, position, played, play_count, favorite, last_played, rating, planned, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, item_id) DO UPDATE SET position = excluded.position, played = excluded.played,
			play_count = excluded.play_count, favorite = excluded.favorite, last_played = excluded.last_played,
			rating = excluded.rating, planned = excluded.planned, note = excluded.note`,
		user, item, p.Position, p.Played, p.PlayCount, p.Favorite, unixTime(p.LastPlayed), p.Rating, unixTime(p.Planned), p.Note)
	return err
}

func (a *Auth) find(name string) *User {
	for _, u := range a.users {
		if strings.EqualFold(u.Name, name) {
			return u
		}
	}
	return nil
}

func (a *Auth) known(id string) bool {
	for _, u := range a.users {
		if u.ID == id {
			return true
		}
	}
	return false
}

func (a *Auth) HasUsers() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.users) > 0
}

func (a *Auth) List() []User {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]User, 0, len(a.users))
	for _, u := range a.users {
		out = append(out, User{ID: u.ID, Name: u.Name, Admin: u.Admin})
	}
	return out
}

// SetUser creates an account or changes the password and role of an
// existing one. An empty password keeps the current one.
func (a *Auth) SetUser(name, password string, admin bool) (*User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a user name is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.find(name)
	if u == nil && len(password) < 4 {
		return nil, errors.New("the password must be at least 4 characters long")
	}
	changed := User{Name: name, Admin: admin}
	if u != nil {
		changed = *u
		changed.Admin = admin
	} else {
		changed.ID = randomHex(16)
	}
	if password != "" {
		changed.Hash = hashPassword(password)
	}
	if _, err := a.conn().Exec(`INSERT INTO users (id, name, hash, admin, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET hash = excluded.hash, admin = excluded.admin`,
		changed.ID, changed.Name, changed.Hash, changed.Admin, time.Now().UnixNano()); err != nil {
		return nil, err
	}
	if u == nil {
		u = &User{}
		a.users = append(a.users, u)
	}
	*u = changed
	return u, nil
}

// DeleteUser removes an account with its tokens, watch states and history;
// the last administrator cannot be removed.
func (a *Auth) DeleteUser(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	admins, idx := 0, -1
	for i, u := range a.users {
		if u.Admin {
			admins++
		}
		if u.ID == id {
			idx = i
		}
	}
	switch {
	case idx < 0:
		return errors.New("no such user")
	case a.users[idx].Admin && admins == 1:
		return errors.New("the last administrator cannot be deleted")
	}
	if _, err := a.conn().Exec(`DELETE FROM users WHERE id = ?`, id); err != nil { // and the rest, by cascade
		return err
	}
	a.users = append(a.users[:idx], a.users[idx+1:]...)
	for token, s := range a.sessions {
		if s.UserID == id {
			delete(a.sessions, token)
		}
	}
	delete(a.watched, id)
	return nil
}

// Login checks the password and returns a new token.
func (a *Auth) Login(name, password, device string) (*User, string, error) {
	a.mu.Lock()
	u := a.find(name)
	hash := "pbkdf2-sha256$1$AAAA$AAAA" // keeps the timing alike for unknown names
	if u != nil {
		hash = u.Hash
	}
	a.mu.Unlock()
	if !checkPassword(hash, password) || u == nil {
		return nil, "", errors.New("wrong user name or password")
	}
	token := randomHex(24)
	s := &session{UserID: u.ID, Device: device, Created: time.Now()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.conn().Exec(`INSERT INTO sessions (token, user_id, device, created) VALUES (?, ?, ?, ?)`, token, s.UserID, s.Device, unixTime(s.Created)); err != nil {
		return nil, "", err
	}
	a.sessions[token] = s
	return u, token, nil
}

func (a *Auth) Logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
	a.conn().Exec(`DELETE FROM sessions WHERE token = ?`, token)
}

// ByToken returns the user a token belongs to, or nil.
func (a *Auth) ByToken(token string) *User {
	if token == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[token]
	if s == nil {
		return nil
	}
	for _, u := range a.users {
		if u.ID == s.UserID {
			return u
		}
	}
	return nil
}

func (a *Auth) Progress(userID, itemID string) Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.watched[userID][itemID]; p != nil {
		return *p
	}
	return Progress{}
}

// Update changes what a user did with a video, series or movie, and writes
// it at once. Somebody without an account (a guest) is not remembered.
func (a *Auth) Update(userID, itemID string, change func(*Progress)) Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := Progress{}
	if old := a.watched[userID][itemID]; old != nil {
		p = *old
	}
	change(&p)
	if !a.known(userID) {
		return p
	}
	if err := writeProgress(a.conn(), userID, itemID, &p); err != nil {
		return p
	}
	if a.watched[userID] == nil {
		a.watched[userID] = map[string]*Progress{}
	}
	a.watched[userID][itemID] = &p
	return p
}

// Watch records the position a player reports in a video of the given
// length; near the end the video counts as watched (and leaves the
// watchlist). The sitting goes into the history.
func (a *Auth) Watch(userID string, v Viewing, position, duration float64) Progress {
	finished := false
	p := a.Update(userID, v.ItemID, func(p *Progress) {
		p.LastPlayed = time.Now()
		switch {
		case duration > 0 && position > duration*0.92:
			if !p.Played {
				p.PlayCount++
			}
			p.Played, p.Position, p.Planned, finished = true, 0, time.Time{}, true
		case position < 30:
			p.Position = 0 // not worth resuming
		default:
			p.Position = position
		}
	})
	a.mu.Lock()
	known := a.known(userID)
	a.mu.Unlock()
	if known {
		a.addToHistory(userID, v, position, duration, finished)
	}
	return p
}

// addToHistory continues the current sitting of the video, or starts one.
// What was watched grows only by what was really played since the last
// report: not by a pause, nor by a jump ahead.
func (a *Auth) addToHistory(userID string, v Viewing, position, duration float64, finished bool) {
	now := time.Now()
	var id, ended int64
	var lastPos, watched float64
	err := a.conn().QueryRow(`SELECT id, ended, position, watched FROM history WHERE user_id = ? AND item_id = ? ORDER BY ended DESC LIMIT 1`,
		userID, v.ItemID).Scan(&id, &ended, &lastPos, &watched)
	if err == nil && now.Sub(fromUnix(ended)) < historySitting {
		elapsed := now.Sub(fromUnix(ended)).Seconds()
		if step := position - lastPos; step > 0 && step <= elapsed+10 {
			watched += step
		}
		a.conn().Exec(`UPDATE history SET ended = ?, position = ?, watched = ?, duration = ?, finished = finished OR ?,
			title = ?, show_title = ?, year = ?, season = ?, episode = ? WHERE id = ?`,
			now.Unix(), position, watched, duration, finished, v.Title, v.ShowTitle, v.Year, v.Season, v.Episode, id)
		return
	}
	a.conn().Exec(`INSERT INTO history (user_id, item_id, show_id, kind, title, year, show_title, season, episode, started, ended, watched, position, duration, finished)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		userID, v.ItemID, v.ShowID, v.Kind, v.Title, v.Year, v.ShowTitle, v.Season, v.Episode, now.Unix(), now.Unix(), position, duration, finished)
}

// HistoryCursor is where a page of the history ends: the next one starts
// after the entry begun at Started with this ID.
type HistoryCursor struct {
	Started, ID int64
}

func (c HistoryCursor) String() string { return fmt.Sprintf("%d.%d", c.Started, c.ID) }

func parseHistoryCursor(s string) HistoryCursor {
	var c HistoryCursor
	fmt.Sscanf(s, "%d.%d", &c.Started, &c.ID)
	return c
}

// History lists a user's sittings, the latest begun first, after the
// cursor (zero: from the latest).
func (a *Auth) History(userID string, after HistoryCursor, limit int) ([]HistoryEntry, error) {
	if after.Started <= 0 {
		after = HistoryCursor{1 << 62, 1 << 62}
	}
	rows, err := a.conn().Query(`SELECT id, item_id, show_id, kind, title, year, show_title, season, episode, started, ended, watched, position, duration, finished
		FROM history WHERE user_id = ? AND (started < ? OR (started = ? AND id < ?)) AND (watched >= ? OR finished)
		ORDER BY started DESC, id DESC LIMIT ?`,
		userID, after.Started, after.Started, after.ID, historyMinimum, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		var started, ended int64
		if err := rows.Scan(&e.ID, &e.ItemID, &e.ShowID, &e.Kind, &e.Title, &e.Year, &e.ShowTitle, &e.Season, &e.Episode,
			&started, &ended, &e.Watched, &e.Position, &e.Duration, &e.Finished); err != nil {
			return nil, err
		}
		e.Started, e.Ended = fromUnix(started), fromUnix(ended)
		out = append(out, e)
	}
	return out, rows.Err()
}

// HistoryStats sums up a user's viewing: all of it, and since a moment.
type HistoryStats struct {
	Total    float64 `json:"total"` // seconds
	Since    float64 `json:"since"` // seconds since the moment asked about
	Titles   int     `json:"titles"`
	Finished int     `json:"finished"`
}

func (a *Auth) Stats(userID string, since time.Time) HistoryStats {
	var s HistoryStats
	a.conn().QueryRow(`SELECT COALESCE(SUM(watched), 0), COALESCE(SUM(CASE WHEN ended >= ? THEN watched END), 0),
		COUNT(DISTINCT CASE WHEN kind = 'episode' THEN show_id ELSE item_id END), COALESCE(SUM(finished), 0)
		FROM history WHERE user_id = ? AND (watched >= ? OR finished)`, since.Unix(), userID, historyMinimum).
		Scan(&s.Total, &s.Since, &s.Titles, &s.Finished)
	return s
}

// ForgetHistory removes one entry of a user's history, or all of it (id 0).
func (a *Auth) ForgetHistory(userID string, id int64) error {
	if id == 0 {
		_, err := a.conn().Exec(`DELETE FROM history WHERE user_id = ?`, userID)
		return err
	}
	res, err := a.conn().Exec(`DELETE FROM history WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// Moved follows titles whose identifiers changed (their files were renamed):
// what users did with them, and their history, now belong to the new ones.
func (a *Auth) Moved(ids map[string]string) {
	if len(ids) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tx, err := a.conn().Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for from, to := range ids {
		if from == to || to == "" {
			continue
		}
		// Where a user already has a state for the new one, it is kept.
		tx.Exec(`UPDATE OR IGNORE watch SET item_id = ? WHERE item_id = ?`, to, from)
		tx.Exec(`DELETE FROM watch WHERE item_id = ?`, from)
		tx.Exec(`UPDATE history SET item_id = ? WHERE item_id = ?`, to, from)
		tx.Exec(`UPDATE history SET show_id = ? WHERE show_id = ?`, to, from)
	}
	if tx.Commit() == nil {
		a.loadWatched()
	}
}
