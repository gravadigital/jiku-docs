package hedgedoc

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gravadigital/jiku-docs/internal/coreapi"
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

func TestReadMappingRelationsAndTopics(t *testing.T) {
	ns := notes()
	file := filepath.Join(t.TempDir(), "mapping.csv")
	sheet := "note,folder,projects,clients,requirements,tasks,topics\n" +
		ns[0].ID + ",/infra/,31;12,4,,900, Reuniones ;Técnico;reuniones\n" +
		ns[1].ID + ",,12,,,,\n"
	if err := os.WriteFile(file, []byte(sheet), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := readMapping(file, NewResolver(ns))
	if err != nil {
		t.Fatal(err)
	}
	a := m[ns[0].ID]
	if fmt.Sprint(a.Relations.Projects, a.Relations.Clients, a.Relations.Tasks) != "[12 31] [4] [900]" ||
		strings.Join(a.Topics, ",") != "reuniones,tecnico" || !a.Decided {
		t.Errorf("%+v", a)
	}
	// Relations alone do not select a note (ns[1] is anonymous: it stays out).
	if b, ok := m[ns[1].ID]; !ok || b.Decided || fmt.Sprint(b.Relations.Projects) != "[12]" {
		t.Errorf("%+v", b)
	}
	for _, bad := range []string{"projects\n" + ns[0].ID + ",x,abc\n", "projects\n" + ns[0].ID + ",x,0\n", "topics\n" + ns[0].ID + ",x,--\n"} {
		bf := filepath.Join(t.TempDir(), "bad.csv")
		_ = os.WriteFile(bf, []byte("note,folder,"+bad), 0o600)
		if _, err := readMapping(bf, NewResolver(ns)); err == nil {
			t.Errorf("sheet %q must fail", bad)
		}
	}
}

type fakeCore map[string][]int64

func (f fakeCore) Entities(_ context.Context, et string, ids []int64) (map[int64]coreapi.Entity, error) {
	out := map[int64]coreapi.Entity{}
	for _, id := range ids {
		for _, ok := range f[et] {
			if ok == id {
				out[id] = coreapi.Entity{ID: id}
			}
		}
	}
	return out, nil
}

func TestDryRunReportsRelationsAndDiscards(t *testing.T) {
	ns := notes()
	dir := t.TempDir()
	sheet := filepath.Join(dir, "mapping.csv")
	report := filepath.Join(dir, "report.csv")
	data := "note,folder,projects,clients,topics\n" + ns[0].ID + ",/infra/,12;99,4,accesos\n"
	if err := os.WriteFile(sheet, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	o := Options{Source: memNotes(ns), Mapping: sheet, Report: report, DryRun: true, CheckRelations: true,
		Relations: fakeCore{"project": {12}, "client": {4}}}
	if err := Run(t.Context(), o, nil, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(report)
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	var row []string
	for _, r := range rows[1:] {
		if r[0] == ns[0].ID {
			row = r
		}
	}
	if row == nil || row[col["status"]] != "planned" || row[col["projects"]] != "12" || row[col["clients"]] != "4" ||
		row[col["topics"]] != "accesos;despliegue;infra" || !strings.Contains(row[col["droppedRelations"]], "project 99: invalid_relation") {
		t.Fatalf("%v", row)
	}
	// Without a checker, --check-relations cannot run.
	o.Relations = nil
	if err := Run(t.Context(), o, nil, logging.Discard()); err == nil {
		t.Error("--check-relations without core must fail")
	}
}
