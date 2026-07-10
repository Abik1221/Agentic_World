BEGIN;

DELETE FROM game_stakes WHERE game IN ('goofspiel', 'monopoly');

COMMIT;
