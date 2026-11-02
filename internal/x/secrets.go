package x

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

type TokenCipher struct{ key []byte }

func NewTokenCipher(encoded string) (TokenCipher, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return TokenCipher{}, errors.New("X_TOKEN_ENCRYPTION_KEY must be base64 of 32 random bytes")
	}
	return TokenCipher{key: key}, nil
}
func (c TokenCipher) Enabled() bool { return len(c.key) == 32 }
func (c TokenCipher) Seal(value string) ([]byte, error) {
	if !c.Enabled() {
		return nil, errors.New("X token encryption key is not configured")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(value), nil), nil
}
func (c TokenCipher) Open(value []byte) (string, error) {
	if !c.Enabled() {
		return "", errors.New("X token encryption key is not configured")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(value) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted X token")
	}
	plain, err := gcm.Open(nil, value[:gcm.NonceSize()], value[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
