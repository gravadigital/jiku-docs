-- Trash instead of bucket versioning (docs/compactacion-historial-v1.md, section 4): the objects
-- of compacted versions are moved to papelera/ before they are deleted, and the trash copy
-- carries the metadata of the version row, so `jiku-docs storage restore-from-trash
-- --version-row` can put the version back in the history. The row is kept here between the
-- commit of the compaction and the move. NULL for rows written before this migration.
ALTER TABLE pruned_objects ADD COLUMN version_row jsonb;
