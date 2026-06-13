// Package keystore provides pluggable credential storage for the higgs CLI.
//
// Two backends are provided:
//   - keyring: backed by the OS keyring (macOS Keychain, Windows Credential
//     Manager, Linux Secret Service via libsecret).
//   - file:    an encrypted file under ~/.higgs/credentials.enc, unlocked by
//     a passphrase supplied via PM_KEYSTORE_PASSPHRASE.
//
// The Default() selector picks the first available backend (keyring, then file,
// then a null backend that errors with guidance).
package keystore

import "errors"

// ServiceName is the service identifier used by the keyring backend. It also
// doubles as the OS-level label so secrets are easy to locate manually.
const ServiceName = "higgs"

// KeyringAccount is the single account key under which the credential pair is
// stored. The CLI talks to one Bridge account, so one pair is sufficient.
const KeyringAccount = "imap"

// ErrNotFound is returned by Get when no credentials exist in the backend.
var ErrNotFound = errors.New("keystore: credentials not found")

// Credentials is the username/password pair stored for the IMAP account.
type Credentials struct {
	Username string
	Password string
}

// Backend is the minimal interface every keystore implementation must satisfy.
//
// Available must be a cheap, non-interactive probe — it is called during
// backend selection and must not prompt or allocate OS resources.
type Backend interface {
	Get() (Credentials, error)
	Set(Credentials) error
	Delete() error
	Name() string
	Available() bool
}

// Default returns the first available backend, preferring the OS keyring,
// falling back to the encrypted file, and finally returning the null backend
// (which errors on every operation with guidance for the user).
func Default() Backend {
	for _, b := range candidates() {
		if b.Available() {
			return b
		}
	}
	return &noneBackend{}
}

// Available returns every backend known to the keystore package, regardless of
// runtime availability. Callers (e.g. `auth status`) can inspect Available()
// on each entry to decide what to report.
func Available() []Backend {
	return candidates()
}

// candidates returns the fixed preference-ordered list of backends. It is
// kept as a function (rather than a package var) so it is always safe to call
// from tests that mutate HOME or environment variables. Tests may override
// the factory via candidatesForTest to exercise selection fallbacks.
func candidates() []Backend {
	if candidatesForTest != nil {
		return candidatesForTest()
	}
	return []Backend{
		newKeyringBackend(),
		newFileBackend(),
	}
}

// candidatesForTest, when non-nil, replaces the backend list returned by
// candidates(). Never set this outside tests.
var candidatesForTest func() []Backend
