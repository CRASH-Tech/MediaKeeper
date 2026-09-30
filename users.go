package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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

// Progress is how far a user has watched a video.
type Progress struct {
	Position   float64   `json:"position"` // seconds
	Played     bool      `json:"played"`
	PlayCount  int       `json:"play_count,omitempty"`
	Favorite   bool      `json:"favorite,omitempty"`
	LastPlayed time.Time `json:"last_played"`
}

type session struct {
	UserID  string    `json:"user"`
	Device  string    `json:"device,omitempty"`
	Created time.Time `json:"created"`
}

// Auth keeps users, their login tokens and watch progress in one file next
// to the settings. Tokens are stored, so clients stay signed in across
// restarts.
type Auth struct {
	path string

	mu       sync.Mutex
	ServerID string                          `json:"server_id"`
	Users    []*User                         `json:"users"`
	Sessions map[string]*session             `json:"sessions"` // by token
	Watched  map[string]map[string]*Progress `json:"watched"`  // user -> video
	dirty    bool
}

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

// OpenAuth loads the account file, creating it on first use.
func OpenAuth(path string) (*Auth, error) {
	a := &Auth{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, a); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if a.ServerID == "" {
		a.ServerID, a.dirty = randomHex(16), true
	}
	if a.Sessions == nil {
		a.Sessions = map[string]*session{}
	}
	if a.Watched == nil {
		a.Watched = map[string]map[string]*Progress{}
	}
	return a, a.Save()
}

// Save writes the file if something has changed.
func (a *Auth) Save() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.dirty {
		return nil
	}
	data, err := json.MarshalIndent(a, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	a.dirty = false
	return nil
}

func (a *Auth) find(name string) *User {
	for _, u := range a.Users {
		if strings.EqualFold(u.Name, name) {
			return u
		}
	}
	return nil
}

func (a *Auth) HasUsers() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.Users) > 0
}

func (a *Auth) List() []User {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]User, 0, len(a.Users))
	for _, u := range a.Users {
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
	u := a.find(name)
	switch {
	case u == nil && len(password) < 4:
		a.mu.Unlock()
		return nil, errors.New("the password must be at least 4 characters long")
	case u == nil:
		u = &User{ID: randomHex(16), Name: name}
		a.Users = append(a.Users, u)
	}
	if password != "" {
		u.Hash = hashPassword(password)
	}
	u.Admin = admin
	a.dirty = true
	a.mu.Unlock()
	return u, a.Save()
}

// DeleteUser removes an account with its tokens; the last administrator
// cannot be removed.
func (a *Auth) DeleteUser(id string) error {
	a.mu.Lock()
	admins, idx := 0, -1
	for i, u := range a.Users {
		if u.Admin {
			admins++
		}
		if u.ID == id {
			idx = i
		}
	}
	switch {
	case idx < 0:
		a.mu.Unlock()
		return errors.New("no such user")
	case a.Users[idx].Admin && admins == 1:
		a.mu.Unlock()
		return errors.New("the last administrator cannot be deleted")
	}
	a.Users = append(a.Users[:idx], a.Users[idx+1:]...)
	for token, s := range a.Sessions {
		if s.UserID == id {
			delete(a.Sessions, token)
		}
	}
	delete(a.Watched, id)
	a.dirty = true
	a.mu.Unlock()
	return a.Save()
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
	a.mu.Lock()
	a.Sessions[token] = &session{UserID: u.ID, Device: device, Created: time.Now()}
	a.dirty = true
	a.mu.Unlock()
	return u, token, a.Save()
}

func (a *Auth) Logout(token string) {
	a.mu.Lock()
	delete(a.Sessions, token)
	a.dirty = true
	a.mu.Unlock()
	a.Save()
}

// ByToken returns the user a token belongs to, or nil.
func (a *Auth) ByToken(token string) *User {
	if token == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.Sessions[token]
	if s == nil {
		return nil
	}
	for _, u := range a.Users {
		if u.ID == s.UserID {
			return u
		}
	}
	return nil
}

func (a *Auth) Progress(userID, itemID string) Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.Watched[userID][itemID]; p != nil {
		return *p
	}
	return Progress{}
}

// Update changes the watch state of a video. It is written to disk by the
// periodic Save, not on every progress report of a player.
func (a *Auth) Update(userID, itemID string, change func(*Progress)) Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Watched[userID] == nil {
		a.Watched[userID] = map[string]*Progress{}
	}
	p := a.Watched[userID][itemID]
	if p == nil {
		p = &Progress{}
		a.Watched[userID][itemID] = p
	}
	change(p)
	a.dirty = true
	return *p
}

// Watch records the position reached in a video of the given length; near
// the end the video counts as watched.
func (a *Auth) Watch(userID, itemID string, position, duration float64) Progress {
	return a.Update(userID, itemID, func(p *Progress) {
		p.LastPlayed = time.Now()
		switch {
		case duration > 0 && position > duration*0.92:
			if !p.Played {
				p.PlayCount++
			}
			p.Played, p.Position = true, 0
		case position < 30:
			p.Position = 0 // not worth resuming
		default:
			p.Position = position
		}
	})
}
