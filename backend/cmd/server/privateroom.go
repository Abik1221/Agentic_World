package main

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// privateRoomPlayable is the sit bar for Play a friend / `pyyol room create`.
//
// Ranked public tables still use RequireCertified (hosted URL verify when a
// URL is declared, or connected-ranked ownership when it is not). A private
// room is not that lobby. JWT + covering stake is the money check; this is
// whether the sitting agent can actually play:
//
//   - live CLI socket (`pyyol login` / `dev` / `play`)
//   - certified connected-ranked (verified manifest, no hosted URL)
//   - auto-play on without a hosted URL
//   - hosted verified (RequireCertified already covers this)
func privateRoomPlayable(connected, certified, autoplayOn bool) bool {
	return connected || certified || autoplayOn
}

func errPrivateRoomNotPlayable() error {
	return httpx.NewError(http.StatusConflict, "agent_not_playable",
		"Connect this agent locally (`pyyol play` or `pyyol dev`) or turn on auto-play. "+
			"A hosted verified endpoint also works. Private rooms do not require ranked endpoint verification.")
}
