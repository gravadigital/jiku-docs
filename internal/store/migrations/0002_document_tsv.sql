-- Search matches whole documents: a query like `aduana -carga` must exclude a document whose
-- other section mentions `carga`, which a per-fragment match cannot see. The fragments stay for
-- ranking the best snippet; the document-level vector decides what matches.

CREATE AGGREGATE tsvector_agg(tsvector) (SFUNC = tsvector_concat, STYPE = tsvector, INITCOND = '');

ALTER TABLE index_status ADD COLUMN doc_tsv tsvector NOT NULL DEFAULT ''::tsvector;
UPDATE index_status s SET doc_tsv = COALESCE((SELECT tsvector_agg(c.tsv ORDER BY c.chunk_no) FROM index_chunks c
  WHERE c.document_id = s.document_id), ''::tsvector);
CREATE INDEX index_status_tsv ON index_status USING gin (doc_tsv);
