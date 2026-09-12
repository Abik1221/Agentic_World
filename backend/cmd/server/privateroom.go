package main

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// privateRoomPlayable is the sit bar for Play a friend / `pyyol room create`.
//
// Same reachability as ranked: a live CLI socket, or a hosted verified URL.
// A no-URL cert or auto-play-on is not a play path — opening a staked room
// then hoping someone connects is how seats forfeit. Start `pyyol play` first.
func privateRoomPlayable(connected, hostedVerified bool) bool {
	return rankedPlayable(connected, hostedVerified)
}

func errPrivateRoomNotPlayable() error {
	return httpx.NewError(http.StatusConflict, "agent_not_playable",
		"Start `pyyol play` or `pyyol dev` first so this agent is connected, then open the room. "+
			"A hosted verified endpoint also works when you are away. Auto-play alone is not a play path. "+
			"Private rooms do not require ranked endpoint verification.")
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
