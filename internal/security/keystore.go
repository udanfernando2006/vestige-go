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
//  1. A fallback file already existing under fallbackDir — see
//     "CodeRabbit-flagged" note below for why this now comes FIRST.
//  2. OS keychain (github.com/zalando/go-keyring — Credential Manager on
//     Windows, Keychain on macOS, Secret Service on Linux). Preferred: the
//     key becomes bound to this OS user account on this machine (DPAPI's
//     actual guarantee), so a copied file alone doesn't work elsewhere.
//  3. A 0600 file under fallbackDir, only if the keychain is genuinely
//     unavailable (no backend present) — NOT used just because a lookup
//     returned "not found"; that case still writes to the keychain.
//
// fallbackDir must already exist or be creatable by the caller — this
// function does not decide what that directory is (cmd/vestige supplies
// the default via defaultDataDir).
//
// CodeRabbit-flagged: the key source could previously flip silently
// between runs. Sequence that caused it: run 1 succeeds via the keychain,
// key stored there. Run 2, weeks later, keyring.Get fails for a
// TRANSIENT reason (a Credential Manager service hiccup, a permissions
// blip — anything other than "genuinely no backend") — this code has no
// way to distinguish that from real unavailability, so it fell through to
// loadOrGenerateFileKey, found no file yet (the real key lives in the
// keychain, untouched), and silently GENERATED AND PERSISTED A NEW,
// DIFFERENT key to the file fallback. From that point on the app used the
// new file key while the old keychain key sat orphaned — any setting
// encrypted under the old key (SELECTOR_API_KEY, DIRECT_API_KEY) became
// permanently undecryptable, surfacing later as a confusing, disconnected
// LLM-auth failure with no obvious link back to a one-time keychain
// hiccup.
//
// Fixed by checking for an already-existing fallback file FIRST, before
// ever touching the keychain: once a file key exists on disk (whether
// from genuine keychain unavailability or a past transient flip), that's
// a strong signal this install is already in file-fallback mode, and
// staying consistent with a PREVIOUS run matters more than re-attempting
// the keychain on every single launch. This doesn't fully solve every
// possible flip direction (a keychain-stored key that later becomes
// permanently unreadable still has no automatic recovery path — by
// design, since silently generating a replacement key is exactly the
// data-loss behavior being fixed here), but it does close the specific,
// confirmed silent-flip case: a fallback file, once it exists, is never
// silently bypassed or overwritten by a fresh keychain attempt again.
func LoadOrGenerateKey(fallbackDir string) (keyB64 string, source KeySource, err error) {
	// --- Attempt 0: an existing fallback file takes priority over a
	// fresh keychain attempt — see the CodeRabbit-flagged note above. ---
	if fallbackDir != "" {
		if existing, readErr := readExistingFileKey(fallbackDir); readErr == nil && existing != "" {
			return existing, KeySourceFile, nil
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			// The file exists but couldn't be read (permissions, corruption
			// from an old pre-atomic-write install, etc.) — this is a real
			// problem worth surfacing rather than silently falling through
			// to the keychain and potentially generating yet another
			// divergent key.
			return "", KeySourceFile, fmt.Errorf("security: read existing fallback key file: %w", readErr)
		}
		// No file yet (os.ErrNotExist) — fall through to the keychain,
		// exactly as before.
	}

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

// readExistingFileKey reads the fallback key file if present, without
// generating one — used by LoadOrGenerateKey's new priority check above.
// Returns os.ErrNotExist (wrapped) unchanged so the caller can
// distinguish "no file yet" from a real read failure.
func readExistingFileKey(fallbackDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(fallbackDir, fallbackFileName))
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
	//
	// CodeRabbit-flagged: this used to write directly to `path` with a
	// single os.WriteFile call — a process kill mid-write (power loss,
	// forced termination) could leave a truncated/corrupted key file,
	// which the NEXT launch would read as-is, either failing cipher
	// construction outright or, worse, silently producing garbage
	// decryption of every stored setting. Fixed the same way schema.go's
	// equivalent crash-safety issue was: write to a temp file in the same
	// directory (so the rename stays on one filesystem, required for
	// atomicity), then os.Rename into place — a failed/interrupted write
	// never leaves anything at the real path for a future launch to read.
	tmpPath := path + ".tmp"
	_ = os.Remove(tmpPath) // best-effort cleanup of a stray file from a prior interrupted attempt
	if err := os.WriteFile(tmpPath, []byte(newKey), 0600); err != nil {
		_ = os.Remove(tmpPath)
		return "", KeySourceFile, fmt.Errorf("security: write fallback key file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", KeySourceFile, fmt.Errorf("security: rename fallback key file into place: %w", err)
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
