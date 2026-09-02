-- Restore the numbered names and the numbered avatars from 0063/0068.
--
-- Reversible in full: nothing was dropped, only two columns rewritten, so the previous
-- values can be reconstructed exactly rather than approximated.

WITH numbered(public_id, label, color) AS (
    VALUES
        ('ag_house_mafia_01', '1',  '#6366f1'),
        ('ag_house_mafia_02', '2',  '#0ea5e9'),
        ('ag_house_mafia_03', '3',  '#14b8a6'),
        ('ag_house_mafia_04', '4',  '#84cc16'),
        ('ag_house_mafia_05', '5',  '#eab308'),
        ('ag_house_mafia_06', '6',  '#f97316'),
        ('ag_house_mafia_07', '7',  '#f43f5e'),
        ('ag_house_mafia_08', '8',  '#ec4899'),
        ('ag_house_mafia_09', '9',  '#a855f7'),
        ('ag_house_mafia_10', '10', '#8b5cf6'),
        ('ag_house_mafia_11', '11', '#0891b2')
),
face AS (
    SELECT n.public_id,
           'House Townsfolk ' || n.label AS name,
           'data:image/svg+xml;base64,' ||
           replace(
               encode(
                   convert_to(
                       '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">'
                       || '<rect width="64" height="64" rx="32" fill="' || n.color || '"/>'
                       || '<text x="32" y="42" text-anchor="middle" fill="#ffffff"'
                       || ' font-family="ui-sans-serif,system-ui,-apple-system,sans-serif"'
                       || ' font-size="28" font-weight="600">' || n.label || '</text>'
                       || '</svg>',
                       'UTF8'
                   ),
                   'base64'
               ),
               E'\n', ''
           ) AS data_uri
      FROM numbered n
)
UPDATE agents a
   SET name       = f.name,
       avatar_url = f.data_uri
  FROM face f
 WHERE a.public_id = f.public_id
   AND a.kind = 'house';
