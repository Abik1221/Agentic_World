package main

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// privateRoomPlayable is the sit bar for Play a friend / `pyyol room create`.
//
// JWT + covering stake is the money check; this is whether the sitting agent
// can actually play. A live CLI socket is enough — rooms never required a
// hosted URL. Hosted verify and auto-play remain alternatives.
func privateRoomPlayable(connected, certified, autoplayOn bool) bool {
	return connected || certified || autoplayOn
}

func errPrivateRoomNotPlayable() error {
	return httpx.NewError(http.StatusConflict, "agent_not_playable",
		"Connect this agent locally (`pyyol play` or `pyyol dev`) or turn on auto-play. "+
			"A hosted verified endpoint also works. Private rooms do not require ranked endpoint verification.")
}

// rankedPlayable is the sit bar for paid / ranked tables and the ranked queue.
//
// A connected local CLI socket (`pyyol play` / `dev`) is first-class — no
// hosted endpoint verify. Hosted verify is the away path when there is no
// local process. Do not require both. Nothing reachable is a refuse.
func rankedPlayable(connected, hostedVerified bool) bool {
	return connected || hostedVerified
}

func errRankedNotPlayable() error {
	return httpx.NewError(http.StatusConflict, "agent_not_playable",
		"Connect this agent locally (`pyyol play` or `pyyol dev`) to sit a ranked table. "+
			"A hosted verified endpoint also works when you are away. Ranked does not require both.")
}
