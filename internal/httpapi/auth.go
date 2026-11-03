package httpapi

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "reposter_session"
const sessionLifetime = 24 * time.Hour

type Authenticator struct {
	iterations int
	salt       []byte
	password   []byte
	key        []byte
}

// NewAuthenticator hashes the configured password in memory on startup.
func NewAuthenticator(plainPassword, encodedKey string) (*Authenticator, error) {
	if len(plainPassword) < 16 || len(plainPassword) > 1024 {
		return nil, errors.New("PANEL_PASSWORD must be 16..1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	const iterations = 310_000
	password, err := pbkdf2.Key(sha256.New, plainPassword, salt, iterations, 32)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(encodedKey)
	if err != nil || len(key) < 32 {
		return nil, errors.New("PANEL_SESSION_KEY must be at least 32 random bytes in hex")
	}
	sessionKey := hmac.New(sha256.New, key)
	_, _ = sessionKey.Write([]byte(plainPassword))
	return &Authenticator{iterations: iterations, salt: salt, password: password, key: sessionKey.Sum(nil)}, nil
}

func (a *Authenticator) CheckPassword(candidate string) bool {
	if len(candidate) > 1024 {
		return false
	}
	digest, err := pbkdf2.Key(sha256.New, candidate, a.salt, a.iterations, 32)
	return err == nil && subtle.ConstantTimeCompare(digest, a.password) == 1
}

func (a *Authenticator) IssueSession(w http.ResponseWriter, r *http.Request) error {
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	expires := time.Now().Add(sessionLifetime)
	value := "v1." + strconv.FormatInt(expires.Unix(), 10) + "." + hex.EncodeToString(nonce)
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(value))
	value += "." + hex.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteStrictMode, Expires: expires})
	return nil
}

func (a *Authenticator) HasSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return false
	}
	if nonce, err := hex.DecodeString(parts[2]); err != nil || len(nonce) != 24 {
		return false
	}
	signature, err := hex.DecodeString(parts[3])
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(strings.Join(parts[:3], ".")))
	return hmac.Equal(signature, mac.Sum(nil))
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func sameOrigin(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	u, err := url.Parse(raw)
	if raw == "" || err != nil || u.Host != r.Host || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return u.Scheme == scheme
}
