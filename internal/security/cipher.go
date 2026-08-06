// Package security provides the settings-encryption boundary.
// Direct port of scraper/security/crypto.py's SettingsCipher.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// nonceSize matches crypto.py's os.urandom(12) — 12 bytes is also
// cipher.NewGCM's own standard nonce size, so this is stated explicitly
// rather than relying on gcm.NonceSize() silently agreeing with Python.
const nonceSize = 12

// SettingsCipher is a direct port of crypto.py's SettingsCipher. Implements
// store.Cipher (internal/store/store.go) — same Encrypt/Decrypt shape,
// same wire format: urlsafe-base64(nonce || ciphertext), no AAD.
type SettingsCipher struct {
	aead cipher.AEAD
}

// NewSettingsCipher mirrors crypto.py's __init__: keyB64 must urlsafe-base64
// decode to exactly 32 bytes (AES-256). Matches the Python source's error
// messages closely enough to be recognizable if surfaced to a user, without
// being a literal string port.
func NewSettingsCipher(keyB64 string) (*SettingsCipher, error) {
	if keyB64 == "" {
		return nil, fmt.Errorf("security: SettingsCipher requires a non-empty key")
	}
	key, err := base64.URLEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("security: SETTINGS_ENCRYPTION_KEY is not valid urlsafe-base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("security: SETTINGS_ENCRYPTION_KEY must decode to exactly 32 bytes (AES-256), got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("security: build AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("security: build GCM: %w", err)
	}
	if aead.NonceSize() != nonceSize {
		// Defensive only — crypto/cipher's GCM nonce size is 12 by
		// construction via NewGCM, this can't actually fire. Kept as an
		// explicit check rather than a silent assumption, since a silent
		// mismatch here would corrupt every encrypt/decrypt call.
		return nil, fmt.Errorf("security: unexpected GCM nonce size %d, want %d", aead.NonceSize(), nonceSize)
	}

	return &SettingsCipher{aead: aead}, nil
}

// Encrypt mirrors crypto.py's encrypt(): random 12-byte nonce per call,
// nonce || ciphertext concatenated, urlsafe-base64 encoded. No AAD (nil),
// matching the Python source's aesgcm.encrypt(nonce, plaintext, None).
func (c *SettingsCipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("security: encrypt: generate nonce: %w", err)
	}

	ciphertext := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	token := append(nonce, ciphertext...)
	return base64.URLEncoding.EncodeToString(token), nil
}

// Decrypt mirrors crypto.py's decrypt(): split the first 12 bytes back out
// as the nonce, decrypt the remainder.
func (c *SettingsCipher) Decrypt(token string) (string, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("security: decrypt: invalid base64: %w", err)
	}
	if len(raw) < nonceSize {
		return "", fmt.Errorf("security: decrypt: token too short to contain a nonce")
	}

	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("security: decrypt: %w", err)
	}
	return string(plaintext), nil
}
