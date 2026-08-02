-- 0068_house_agent_avatars — give every deterministic (house) agent a face.
--
-- House agents fill sandbox seats: 3 Goofspiel/Monopoly difficulty bots and the 11
-- Mafia townsfolk. None of them had avatar_url set, so every one of them rendered as
-- the same anonymous placeholder — in a 12-seat Mafia table that means eleven
-- identical blanks and no way to track who said what across a discussion round.
--
-- Avatars are self-contained SVG data URIs rather than links:
--   * no external host, so no third-party request, no CDN dependency and nothing
--     to break if that host disappears;
--   * no new endpoint or bucket to serve them from;
--   * base64 rather than percent-encoded, because a raw '#' in a colour literal
--     terminates a data URI at the fragment and would silently yield a broken image.
--     Postgres wraps encode(...,'base64') at 76 chars, so newlines are stripped.
--
-- Colours and labels are assigned explicitly, not hashed: a hash gives no control
-- over contrast and can hand two adjacent seats near-identical colours, which
-- defeats the entire point of telling seats apart. Every colour here carries white
-- text legibly.
--
-- Idempotent twice over: only fills where the avatar is missing, and re-running
-- changes nothing. Never overwrites an avatar someone deliberately set.

WITH face(public_id, label, color) AS (
    VALUES
        -- Goofspiel / Monopoly difficulty ladder: green → amber → red reads as
        -- rising difficulty at a glance.
        ('ag_house_rookie',     'R',  '#22c55e'),
        ('ag_house_challenger', 'C',  '#f59e0b'),
        ('ag_house_master',     'M',  '#ef4444'),
        -- Mafia table: 11 seats, hues spread around the wheel so no two seats sit
        -- close together in colour.
        ('ag_house_mafia_01',   '1',  '#6366f1'),
        ('ag_house_mafia_02',   '2',  '#0ea5e9'),
        ('ag_house_mafia_03',   '3',  '#14b8a6'),
        ('ag_house_mafia_04',   '4',  '#84cc16'),
        ('ag_house_mafia_05',   '5',  '#eab308'),
        ('ag_house_mafia_06',   '6',  '#f97316'),
        ('ag_house_mafia_07',   '7',  '#f43f5e'),
        ('ag_house_mafia_08',   '8',  '#ec4899'),
        ('ag_house_mafia_09',   '9',  '#a855f7'),
        ('ag_house_mafia_10',   '10', '#8b5cf6'),
        ('ag_house_mafia_11',   '11', '#0891b2')
),
svg AS (
    SELECT f.public_id,
           'data:image/svg+xml;base64,' ||
           replace(
               encode(
                   convert_to(
                       '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">'
                       || '<rect width="64" height="64" rx="32" fill="' || f.color || '"/>'
                       || '<text x="32" y="42" text-anchor="middle" fill="#ffffff"'
                       || ' font-family="ui-sans-serif,system-ui,-apple-system,sans-serif"'
                       -- Two-digit seats get a smaller glyph so "10"/"11" still fit the circle.
                       || ' font-size="' || CASE WHEN length(f.label) > 1 THEN '22' ELSE '28' END || '"'
                       || ' font-weight="600">' || f.label || '</text>'
                       || '</svg>',
                       'UTF8'
                   ),
                   'base64'
               ),
               E'\n', ''
           ) AS data_uri
    FROM face f
)
UPDATE agents a
SET avatar_url = s.data_uri
FROM svg s
WHERE a.public_id = s.public_id
  AND a.kind = 'house'
  AND (a.avatar_url IS NULL OR a.avatar_url = '');
