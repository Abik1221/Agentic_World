DROP TABLE IF EXISTS debts;
DELETE FROM wallets WHERE kind = 'bad_debt' AND agent_id IS NULL;
