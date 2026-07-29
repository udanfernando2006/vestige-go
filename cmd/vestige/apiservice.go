package main

// APIService is bound to the frontend the same way GreetService is in the
// Wails scaffold (application.NewService(&APIService{...})) — a plain
// struct, exported methods become the JS/TS-callable surface. Unlike
// GreetService, this one carries state (the real port the Gin listener
// ended up bound to), set once at startup in main() before app.Run(), so
// GetAPIPort() has something real to return the first time the frontend
// calls it — no HTTP round-trip needed, this is Wails' own in-process
// Go<->JS bridge, so it works before the frontend has any idea what port
// the REST API is even listening on.
type APIService struct {
	port int
}

// GetAPIPort returns the real TCP port the Gin server bound to. Called once
// by client.ts at startup; client.ts then builds every REST request against
// http://127.0.0.1:<this port>.
func (a *APIService) GetAPIPort() int {
	return a.port
}
