-- Revert 0063_mafia_house_bots — remove the Mafia house filler bots + their wallets.
DELETE FROM wallets
WHERE agent_id IN (SELECT id FROM agents WHERE public_id LIKE 'ag_house_mafia_%');

DELETE FROM agents WHERE public_id LIKE 'ag_house_mafia_%';
