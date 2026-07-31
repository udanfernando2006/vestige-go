// Package security provides the settings-encryption boundary.
package security

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Fixed identifiers for the OS credential store entry. Not user-configurable —
// changing either after real installs exist would orphan the previously
// stored key, the same class of footgun the migration blueprint already
// flags for Tauri's `identifier` field (vestige_archive_history_catalog.md).
const (
	keyringService = "vestige-go"
	keyringUser    = "settings-encryption-key"

	// fallbackFileName lives under whatever directory the caller passes as
	// fallbackDir (cmd/vestige defaults this to <UserCacheDir>/VestigeGo/keys).
	fallbackFileName = "settings.key"
)

// KeySource reports where the encryption key actually came from, so a
// caller (eventually the Settings/first-run UI) can surface a real warning
// when the weaker path was used, rather than the degradation happening
// silently. Per the earlier conversation: falling back to a plain file is
// an accepted degraded mode for keychain-unavailable environments (e.g.
// minimal Linux without a Secret Service daemon), not a silent failure.
type KeySource int

const (
	// KeySourceKeyring: the OS credential store (Windows Credential
	// Manager/DPAPI, macOS Keychain, Linux Secret Service) was used
	// successfully. This is the only source that provides the
	// machine-and-user-account binding described in conversation — a
	// stolen file copies and works elsewhere; this doesn't.
	KeySourceKeyring KeySource = iota
	// KeySourceFile: the OS keychain was unavailable (no backend, e.g.
	// headless Linux with no Secret Service running) and a 0600 file
	// under fallbackDir was used instead. Weaker: portable if the file
	// is copied off the machine, same as v1's .env-based key ever was.
	KeySourceFile
)

func (k KeySource) String() string {
	switch k {
	case KeySourceKeyring:
		return "os-keyring"
	case KeySourceFile:
		return "file-fallback"
	default:
		return "unknown"
	}
}

// LoadOrGenerateKey returns the app's SETTINGS_ENCRYPTION_KEY as a
// urlsafe-base64 string ready to hand to NewSettingsCipher, generating and
// persisting a fresh one on first run. Replaces v1's build_cipher_from_env()
// (crypto.py) — there is no equivalent env var read here at all; this is
// the single first-run-seeding entry point for Vestige-Go instead.
//
// Order of attempts:
//  1. OS keychain (github.com/zalando/go-keyring — Credential Manager on
//     Windows, Keychain on macOS, Secret Service on Linux). Preferred: the
//     key becomes bound to this OS user account on this machine (DPAPI's
//     actual guarantee), so a copied file alone doesn't work elsewhere.
//  2. A 0600 file under fallbackDir, only if the keychain is genuinely
//     unavailable (no backend present) — NOT used just because a lookup
//     returned "not found"; that case still writes to the keychain.
//
// fallbackDir must already exist or be creatable by the caller — this
// function does not decide what that directory is (cmd/vestige supplies
// the default via defaultDataDir).
func LoadOrGenerateKey(fallbackDir string) (keyB64 string, source KeySource, err error) {
	// --- Attempt 1: OS keychain ---
	existing, kerr := keyring.Get(keyringService, keyringUser)
	switch {
	case kerr == nil:
		return existing, KeySourceKeyring, nil

	case errors.Is(kerr, keyring.ErrNotFound):
		// No key yet, but the keychain backend itself is reachable —
		// generate one and store it there. This is the expected first-run
		// path on a working keychain, not an error condition.
		newKey, genErr := generateKeyB64()
		if genErr != nil {
			return "", KeySourceFile, fmt.Errorf("security: generate key: %w", genErr)
		}
		if setErr := keyring.Set(keyringService, keyringUser, newKey); setErr != nil {
			// Backend was reachable enough to report ErrNotFound a moment
			// ago but now fails to Set — treat as keychain-unavailable
			// and fall through to the file path rather than erroring out
			// the whole app over it.
			return loadOrGenerateFileKey(fallbackDir)
		}
		return newKey, KeySourceKeyring, nil

	default:
		// Some other failure — typically "no keyring backend available"
		// on a minimal Linux install with no Secret Service daemon
		// running. This is the genuinely-unavailable case the file
		// fallback exists for.
		return loadOrGenerateFileKey(fallbackDir)
	}
}

func loadOrGenerateFileKey(fallbackDir string) (string, KeySource, error) {
	if fallbackDir == "" {
		return "", KeySourceFile, fmt.Errorf("security: keychain unavailable and no fallbackDir provided")
	}
	if err := os.MkdirAll(fallbackDir, 0700); err != nil {
		return "", KeySourceFile, fmt.Errorf("security: create fallback dir: %w", err)
	}
	path := filepath.Join(fallbackDir, fallbackFileName)

	raw, readErr := os.ReadFile(path)
	if readErr == nil {
		return string(raw), KeySourceFile, nil
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return "", KeySourceFile, fmt.Errorf("security: read fallback key file: %w", readErr)
	}

	newKey, genErr := generateKeyB64()
	if genErr != nil {
		return "", KeySourceFile, fmt.Errorf("security: generate key: %w", genErr)
	}
	// 0600: readable/writable by the owning OS user only. Still a plain
	// file — no machine/account binding the way keychain storage has —
	// this permission bit only stops OTHER local accounts from reading
	// it, not malware running as the same user, and not someone who
	// copies the whole file off the disk.
	if err := os.WriteFile(path, []byte(newKey), 0600); err != nil {
		return "", KeySourceFile, fmt.Errorf("security: write fallback key file: %w", err)
	}
	return newKey, KeySourceFile, nil
}

func generateKeyB64() (string, error) {
	key := make([]byte, 32) // AES-256, matches SettingsCipher's own requirement exactly
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(key), nil
}
