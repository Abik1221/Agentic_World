-- Clear only the generated house avatars. Scoped to the data-URI form this
-- migration wrote and to kind='house', so a real avatar set on any other agent is
-- never touched.
UPDATE agents
SET avatar_url = NULL
WHERE kind = 'house'
  AND avatar_url LIKE 'data:image/svg+xml;base64,%';
