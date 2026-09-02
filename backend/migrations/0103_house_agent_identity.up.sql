-- Give the Mafia house agents an identity.
--
-- They were "House Townsfolk 1" through "House Townsfolk 11": one name plus a number,
-- which reads as eleven copies of the same thing rather than eleven players. A table of
-- those is the clearest possible signal that nobody at it is real, and it is the first
-- thing anyone sees on a sandbox game.
--
-- Their avatars had the same problem from the other direction. 0068 gave each one a
-- distinct HUE, which was right, and then drew the seat NUMBER in the middle of it — so
-- the picture restated the thing the name already gave away. The hues are kept and the
-- glyph becomes the name's initial.
--
-- # These are still house agents, and nothing here hides that
--
-- kind='house' is untouched, so every control that keys off it still applies: they are
-- excluded from the leaderboard, exempt from certification only at zero stake, and never
-- stake or earn. The UI can label them exactly as it does today.
--
-- What changes is presentation, and the distinction matters: they ARE agents — the server
-- drives them through the same engine API a developer's agent uses — so a real name and a
-- face is an accurate description, not a costume. Making them look like SOMEONE ELSE'S
-- agent would be a different thing, and this does not do that.
--
-- # Names
--
-- Short, distinct, and distinct in their INITIAL too, because the initial is the avatar
-- glyph — two names starting with the same letter would put the same face on two seats and
-- undo half of this.
--
-- public_id and slug are untouched. The ids are referenced by the house roster built at
-- boot (SetHouseRoster) and the slug appears in URLs; renaming either would break a
-- control or a link for a cosmetic change.

WITH named(public_id, name, initial, color) AS (
    VALUES
        ('ag_house_mafia_01', 'Vale',   'V', '#6366f1'),
        ('ag_house_mafia_02', 'Kite',   'K', '#0ea5e9'),
        ('ag_house_mafia_03', 'Bram',   'B', '#14b8a6'),
        ('ag_house_mafia_04', 'Sona',   'S', '#84cc16'),
        ('ag_house_mafia_05', 'Idris',  'I', '#eab308'),
        ('ag_house_mafia_06', 'Lark',   'L', '#f97316'),
        ('ag_house_mafia_07', 'Wren',   'W', '#f43f5e'),
        ('ag_house_mafia_08', 'Osric',  'O', '#ec4899'),
        ('ag_house_mafia_09', 'Thea',   'T', '#a855f7'),
        ('ag_house_mafia_10', 'Corvin', 'C', '#8b5cf6'),
        ('ag_house_mafia_11', 'Nim',    'N', '#0891b2')
),
face AS (
    SELECT n.public_id,
           n.name,
           'data:image/svg+xml;base64,' ||
           replace(
               encode(
                   convert_to(
                       '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">'
                       || '<rect width="64" height="64" rx="32" fill="' || n.color || '"/>'
                       || '<text x="32" y="42" text-anchor="middle" fill="#ffffff"'
                       || ' font-family="ui-sans-serif,system-ui,-apple-system,sans-serif"'
                       || ' font-size="28" font-weight="600">' || n.initial || '</text>'
                       || '</svg>',
                       'UTF8'
                   ),
                   'base64'
               ),
               E'\n', ''
           ) AS data_uri
      FROM named n
)
UPDATE agents a
   SET name       = f.name,
       avatar_url = f.data_uri
  FROM face f
 WHERE a.public_id = f.public_id
   AND a.kind = 'house';

-- Goofspiel's three are deliberately left alone. "House Rookie", "House Challenger" and
-- "House Master" are not a numbered series — they name a DIFFICULTY, which is the thing a
-- developer picks between when choosing a sandbox opponent. Replacing that with a personal
-- name would delete information the player uses.
