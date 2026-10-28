-- P-1, documentation without folders (docs/p1/service.md): the visibility of a document decides
-- who reads it, not the folder ACL.
--
-- - `restricted`: only admins and services. The data migration that sets it on the documents a
--   person with role `user` cannot read today runs in Go at startup (it writes a new version
--   with the frontmatter rewritten), right after this file; see service.MigrateP1.
-- - Names of authors stored on write (`createdByName`, `updatedByName`, `authorName`,
--   `lock.holderName`); the existing rows are completed from core in the background.
-- - Links of a document that becomes restricted are revoked with reason `restricted`.

ALTER TABLE documents DROP CONSTRAINT documents_visibility_check;
ALTER TABLE documents ADD CONSTRAINT documents_visibility_check
  CHECK (visibility IN ('internal', 'public', 'restricted'));
CREATE INDEX documents_visibility ON documents (visibility);

ALTER TABLE documents ADD COLUMN created_by_name text;
ALTER TABLE documents ADD COLUMN updated_by_name text;
ALTER TABLE document_versions ADD COLUMN author_name text;
ALTER TABLE document_locks ADD COLUMN holder_name text;

ALTER TABLE shares DROP CONSTRAINT shares_revoke_reason_check;
ALTER TABLE shares ADD CONSTRAINT shares_revoke_reason_check
  CHECK (revoke_reason IN ('manual', 'document_deleted', 'restricted'));
