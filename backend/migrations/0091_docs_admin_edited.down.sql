-- Dropping the column loses which pages an admin owns, so the next boot seeder reclaims
-- every one of them. That is the documented cost of rolling this back, not an oversight.
ALTER TABLE docs_pages DROP COLUMN IF EXISTS admin_edited;
