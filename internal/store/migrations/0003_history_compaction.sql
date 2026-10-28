-- History compaction (docs/history-compaction-v1.md, section 4): every save is still a
-- version, and a daily process thins out the old ones. These columns say which versions can
-- never go, and which documents have new activity since their last compaction.

-- Origin of each version, to protect creations and restorations. `documents.move` creates no
-- version (it only emits document.moved), so there is no kind 'move'.
ALTER TABLE document_versions
  ADD COLUMN kind text NOT NULL DEFAULT 'update'
    CHECK (kind IN ('create','update','restore'));

-- Permanent protection, computed on insert: create, restore, with message, via service. The
-- protection of a version pinned by an active share is dynamic and is not stored here.
ALTER TABLE document_versions
  ADD COLUMN protected boolean NOT NULL DEFAULT false;

-- Existing data. Restorations are recognised by the fixed message the service writes.
UPDATE document_versions SET kind = 'create' WHERE version = 1;
UPDATE document_versions SET kind = 'restore' WHERE version > 1 AND message LIKE 'Restaurado desde la versión %';
UPDATE document_versions SET protected = true
  WHERE kind IN ('create','restore') OR message IS NOT NULL OR via = 'service';

-- Compaction state per document.
ALTER TABLE documents
  ADD COLUMN last_compacted_at timestamptz,
  ADD COLUMN pruned_versions_count integer NOT NULL DEFAULT 0;

CREATE INDEX documents_pending_compaction
  ON documents (id)
  WHERE deleted_at IS NULL AND (last_compacted_at IS NULL OR updated_at > last_compacted_at);

-- Pinned shares, to find the documents whose pinned link expired or was revoked since their
-- last compaction (the pinned version becomes a candidate again).
CREATE INDEX shares_pinned ON shares (document_id) WHERE mode = 'pinned';

-- Objects of compacted versions, deleted from S3 after the commit. A row goes away once its
-- object is deleted (or was already gone).
CREATE TABLE pruned_objects (
  storage_key text PRIMARY KEY,
  document_id text NOT NULL,
  version     integer NOT NULL,
  pruned_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pruned_objects_age ON pruned_objects (pruned_at);
