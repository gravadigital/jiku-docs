package service

import (
	"strings"
	"testing"
	"time"
)

// The version row travels as S3 user metadata: base64 JSON, under 2 KB. A title too long for
// that is dropped (the restore takes the document's current one).
func TestVersionRowMetadata(t *testing.T) {
	raw := []byte(`{"documentId":"01J","version":3,"checksum":"sha256:x","sizeBytes":10,"title":"Título ñ","author":"u1",
		"via":"human","sessionId":null,"message":null,"kind":"update","createdAt":"2026-10-22T06:00:00Z"}`)
	meta := encodeVersionRow(raw)
	r, err := decodeVersionRow(meta)
	if err != nil || r.DocumentID != "01J" || r.Version != 3 || r.Title != "Título ñ" || r.Via != "human" ||
		!r.CreatedAt.Equal(time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", r, err)
	}
	long := strings.Replace(string(raw), "Título ñ", strings.Repeat("ñ", 2000), 1)
	meta = encodeVersionRow([]byte(long))
	if len(meta[versionRowMeta]) > maxVersionRowMeta {
		t.Fatal("metadata too large")
	}
	if r, err := decodeVersionRow(meta); err != nil || r.Title != "" || r.Author != "u1" {
		t.Fatalf("%+v %v", r, err)
	}
	if encodeVersionRow(nil) != nil {
		t.Fatal("no row, no metadata")
	}
	if _, err := decodeVersionRow(map[string]string{}); err == nil {
		t.Fatal("a copy without row cannot restore a version")
	}
}
