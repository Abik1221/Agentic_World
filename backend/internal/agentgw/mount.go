package agentgw

import "github.com/go-chi/chi/v5"

// ConnectPath is the route a local agent's SDK dials to open its socket.
const ConnectPath = "/v1/agent/connect"

// Register mounts the WebSocket connect endpoint. It matches the repo's
// Mount(func(chi.Router)) convention so it slots into httpx.NewRouter alongside
// the other handlers. The upgrade is a GET; auth happens in the register frame
// (the Authenticator), not via the normal bearer middleware, because a WebSocket
// client authenticates after the upgrade.
func (g *Gateway) Register(r chi.Router) {
	r.Get(ConnectPath, g.Handler())
}
