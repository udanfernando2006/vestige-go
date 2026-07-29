// Package frontend exists only to satisfy Go's embed directive, which
// cannot reach a sibling directory (cmd/vestige/main.go -> ../../frontend/dist
// is not valid embed syntax — embed patterns may not use ..). Assets is
// embedded here, next to frontend/dist itself, and imported by
// cmd/vestige/main.go instead. Requires frontend/dist to actually exist at
// build time — i.e. the frontend build (task build:frontend, or whatever
// Taskfile task runs vite build) must run before this package compiles,
// same precondition the scratchtest scaffold's own
// //go:embed all:frontend/dist has, just satisfied from a different file
// since main.go isn't at the project root here.
package frontend

import "embed"

//go:embed all:dist
var Assets embed.FS
