package hedgedoc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/logging"
)

type memNotes []Note

func (m memNotes) Notes(context.Context) ([]Note, error) { return m, nil }

func TestReadMappingIgnoresBlankRows(t *testing.T) {
	ns := notes()
	file := filepath.Join(t.TempDir(), "mapping.csv")
	sheet := "note,title,folder,fileName,skip,confianza\n" +
		ns[0].ID + ",Guía,infra/servidores,,,alta\n" +
		ns[1].ID + ",Notas,,,,baja\n"
	if err := os.WriteFile(file, []byte(sheet), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := readMapping(file, NewResolver(ns))
	if err != nil {
		t.Fatal(err)
	}
	if got := m[ns[0].ID].Folder; got != "/infra/servidores/" {
		t.Errorf("folder = %q", got)
	}
	if _, ok := m[ns[1].ID]; ok {
		t.Error("a blank row (no folder, fileName or skip) must not count as mapped")
	}
}

func TestExportedSheetDoesNotSelectAnonymousNotes(t *testing.T) {
	ns := notes() // ns[1] is anonymous
	dir := t.TempDir()
	sheet := filepath.Join(dir, "mapping.csv")
	src := memNotes(ns)
	if err := Run(t.Context(), Options{Source: src, ExportMapping: sheet}, nil, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	m, err := readMapping(sheet, NewResolver(ns))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("an untouched exported sheet must map nothing, got %d", len(m))
	}
}
