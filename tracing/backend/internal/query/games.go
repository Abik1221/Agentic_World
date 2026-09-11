package query

import "strings"

// Live arena games Eye will list. Monopoly was withdrawn from the product; historical
// mp_* / session_id=monopoly telemetry is excluded rather than shown as a live game.
var liveGames = map[string]bool{
	"goofspiel": true,
	"mafia":     true,
}

func isMonopolyID(id string) bool {
	id = strings.TrimPrefix(id, "match_")
	return strings.HasPrefix(id, "mp_") || strings.EqualFold(id, "monopoly")
}

func gameFromMatchID(id, session string) string {
	if session != "" && !strings.EqualFold(session, "monopoly") {
		return session
	}
	id = strings.TrimPrefix(id, "match_")
	switch {
	case strings.HasPrefix(id, "mf_"):
		return "mafia"
	case strings.HasPrefix(id, "mp_"):
		return "monopoly"
	case strings.HasPrefix(id, "m_"):
		return "goofspiel"
	default:
		return session
	}
}

func isLiveGame(game string) bool {
	return liveGames[strings.ToLower(strings.TrimSpace(game))]
}

// monopolySQL excludes withdrawn Monopoly rows. Bound as a fragment in WHERE.
const monopolySQL = `
	AND run_id NOT LIKE 'mp\\_%'
	AND trace_id NOT LIKE 'match_mp\\_%'
	AND session_id != 'monopoly'`
