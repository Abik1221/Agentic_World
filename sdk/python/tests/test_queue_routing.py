"""Which matchmaking queue each game goes to.

Goofspiel is 1v1 and uses /v1/queue; Mafia and Monopoly are N-player and pool into a
full table via /v1/group-queue. The request shape is identical, so sending a game to
the wrong endpoint fails at the server with a confusing message rather than locally.

This is regression cover for a real bug: `pyyol play --ranked mafia` hardcoded
/v1/queue, and that queue rejects every game but Goofspiel, so ranked Mafia and
Monopoly could not be entered from the CLI at all.
"""

from pyyol.cli import GROUP_GAMES, queue_path_for


def test_group_games_use_the_group_queue():
    for game in ("mafia",):
        assert queue_path_for(game) == "/v1/group-queue", game


def test_two_player_games_use_the_pair_queue():
    assert queue_path_for("goofspiel") == "/v1/queue"


def test_unknown_games_default_to_the_pair_queue():
    # An unrecognised game is not silently routed to the group queue: the 1v1 queue
    # names the game it serves in its rejection, which is a far more useful error than
    # a group queue timing out waiting for a table that will never fill.
    assert queue_path_for("some-new-game") == "/v1/queue"


def test_every_group_game_is_actually_routed():
    # Guards the pairing itself — adding a game to GROUP_GAMES without the routing
    # honouring it would reintroduce the original bug in a new form.
    for game in GROUP_GAMES:
        assert queue_path_for(game) == "/v1/group-queue", game
