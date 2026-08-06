// Package appicon exists only to satisfy Go's embed directive, which
// cannot reach build/appicon.png from cmd/vestige/main.go (parent-directory
// traversal isn't valid embed syntax — same constraint, same fix shape, as
// frontend/embed.go). Deliberately named appicon rather than build, even
// though that doesn't match its containing folder's name — "package build"
// sitting inside Wails' own build/ tooling directory (Taskfiles, config.yml,
// platform subfolders) seemed more likely to confuse a future reader than
// the harmless name/folder mismatch does.
//
// UNVERIFIED: application.Options.Icon's real expected type. Antigravity's
// suggested fix claims []byte of raw PNG data; I couldn't independently
// confirm that against real Wails v3 source — the docs I could find list
// WebviewWindowOptions' actual fields with no Icon entry, and I didn't find
// application.Options' full struct either. Written on that assumption; if
// `go build` disagrees, the compiler's own error for a wrong-typed struct
// literal will very likely name the real expected type directly.
package appicon

import _ "embed"

//go:embed appicon.png
var IconPNG []byte