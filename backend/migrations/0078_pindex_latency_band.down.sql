BEGIN;
-- Only ever removes the inactive row it added. If an operator has activated v3, dropping
-- it would leave the platform with no active config at all, so that case is left alone
-- deliberately rather than "helpfully" reverting a decision someone made on purpose.
DELETE FROM pindex_config WHERE version = 3 AND NOT active;
COMMIT;
