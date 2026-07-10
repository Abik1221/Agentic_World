package gamestakes

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// Tier-resolution rejections, surfaced to the end user by the play handlers.
var (
	// ErrUnknownTier: the requested tier key is not offered for this game.
	ErrUnknownTier = httpx.NewError(http.StatusBadRequest, "unknown_tier", "That stake tier is not offered for this game.")
	// ErrTierDisabled: the tier exists but the admin has disabled it.
	ErrTierDisabled = httpx.NewError(http.StatusForbidden, "tier_disabled", "That stake tier is currently disabled.")
	// ErrGameNoTiers: the game has no configured tiers at all (callers may fall back
	// to the legacy free-form stake for back-compat).
	ErrGameNoTiers = httpx.NewError(http.StatusBadRequest, "no_tiers", "This game has no configured stake tiers.")
	// ErrTierRequired: the game has configured tiers, so a raw free-form stake is not
	// allowed — the caller must choose a tier.
	ErrTierRequired = httpx.NewError(http.StatusBadRequest, "tier_required", "This game uses fixed stake tiers; choose a tier.")
)

func errInvalid(msg string) error {
	return httpx.NewError(http.StatusBadRequest, "invalid_request", msg)
}
