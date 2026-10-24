package hedgedoc

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/secrets"
	"github.com/gravadigital/jiku-docs/internal/service"
)

// DefaultFolder receives the notes without a mapping. Only administrators can read it.
const DefaultFolder = "/importado/"

// DefaultAssetPrefixes are where HedgeDoc stored images (scheme-less URL prefixes).
var DefaultAssetPrefixes = []string{
	"sfo2.digitaloceanspaces.com/grava-internal-01/docs-grava-io/",
	"grava-internal-01.sfo2.digitaloceanspaces.com/docs-grava-io/",
	"docs.grava.io/uploads/",
}

// Options of a run.
type Options struct {
	DSN              string
	Mapping          string
	Report           string
	ExportMapping    string
	DryRun           bool
	OnlyMapped       bool
	IncludeAnonymous bool
	Limit            int
	HedgeDocURL      string
	AssetPrefixes    []string
	MaxAssetBytes    int64
	// Source overrides the MariaDB source (tests).
	Source Source
	// HTTP downloads assets and uploads them (tests may replace it).
	HTTP *http.Client
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// ParseFlags reads the command line of `jiku-docs import hedgedoc`.
func ParseFlags(args []string) (Options, error) {
	fs := flag.NewFlagSet("import hedgedoc", flag.ContinueOnError)
	var o Options
	var prefixes listFlag
	fs.StringVar(&o.DSN, "mysql-dsn", envOr("HEDGEDOC_MYSQL_DSN", "root:hedgedoc@tcp(127.0.0.1:33306)/codimd?parseTime=true&charset=utf8mb4"),
		"DSN de la base MariaDB cargada con el dump (go-sql-driver/mysql, con parseTime=true)")
	fs.StringVar(&o.Mapping, "mapping", "", "planilla CSV nota -> carpeta (columnas: note, folder[, fileName][, skip])")
	fs.StringVar(&o.Report, "report", ".local/hedgedoc-import-report.csv", "informe CSV de la corrida (estado y posibles secretos por nota)")
	fs.StringVar(&o.ExportMapping, "export-mapping", "", "escribe una planilla de mapeo a completar con todas las notas y sale")
	fs.BoolVar(&o.DryRun, "dry-run", false, "calcula el plan y el informe sin escribir nada ni descargar imágenes")
	fs.BoolVar(&o.OnlyMapped, "only-mapped", false, "importa solo las notas que figuran en la planilla")
	fs.BoolVar(&o.IncludeAnonymous, "include-anonymous", false, "incluye las notas sin dueño (creadas sin sesión; en el dump real son casi todas spam)")
	fs.IntVar(&o.Limit, "limit", 0, "importa como máximo N notas (para probar)")
	fs.StringVar(&o.HedgeDocURL, "hedgedoc-url", "https://docs.grava.io", "URL base de HedgeDoc, para reescribir enlaces entre notas")
	fs.Var(&prefixes, "asset-prefix", "prefijo de URL (sin esquema) de las imágenes a migrar como adjuntos; repetible")
	fs.Int64Var(&o.MaxAssetBytes, "max-asset-bytes", 50<<20, "tamaño máximo de un adjunto")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.AssetPrefixes = prefixes
	if len(o.AssetPrefixes) == 0 {
		o.AssetPrefixes = DefaultAssetPrefixes
	}
	return o, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// mapping is one row of the sheet.
type mapping struct {
	Folder   string
	FileName string
	Skip     bool
}

func normalizeFolder(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return path.Clean(p) + "/"
}

func readMapping(file string, r *Resolver) (map[string]mapping, error) {
	out := map[string]mapping{}
	if file == "" {
		return out, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("mapping: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	noteCol, ok := col["note"]
	if !ok {
		return nil, errors.New("mapping: falta la columna note")
	}
	folderCol, ok := col["folder"]
	if !ok {
		return nil, errors.New("mapping: falta la columna folder")
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("mapping línea %d: %w", line, err)
		}
		if noteCol >= len(rec) || strings.TrimSpace(rec[noteCol]) == "" {
			continue
		}
		n := r.Find(strings.TrimSpace(rec[noteCol]))
		if n == nil {
			return nil, fmt.Errorf("mapping línea %d: la nota %q no existe", line, rec[noteCol])
		}
		m := mapping{FileName: get(rec, "filename")}
		if folderCol < len(rec) {
			m.Folder = normalizeFolder(rec[folderCol])
		}
		switch strings.ToLower(get(rec, "skip")) {
		case "1", "true", "si", "sí", "yes", "x":
			m.Skip = true
		}
		if m.FileName != "" && !service.ValidFileName(m.FileName) {
			return nil, fmt.Errorf("mapping línea %d: fileName %q inválido (tiene que terminar en .md)", line, m.FileName)
		}
		out[n.ID] = m
	}
	return out, nil
}

// planned is what will happen to one note.
type planned struct {
	note       *Note
	docID      string
	folder     string
	fileName   string
	content    string
	assets     []string
	unresolved []string
	findings   []secrets.Finding
	warnings   []string
	status     string
	detail     string
}

// Run executes the import (svc == nil only for --dry-run and --export-mapping).
func Run(ctx context.Context, o Options, svc *service.Service, log *slog.Logger) error {
	src := o.Source
	if src == nil {
		src = MySQLSource{DSN: o.DSN}
	}
	all, err := src.Notes(ctx)
	if err != nil {
		return fmt.Errorf("reading HedgeDoc: %w", err)
	}
	resolver := NewResolver(all)
	if o.ExportMapping != "" {
		return exportMapping(o.ExportMapping, all, log)
	}
	maps, err := readMapping(o.Mapping, resolver)
	if err != nil {
		return err
	}
	webURL := ""
	if svc != nil {
		webURL = svc.Set.WebURL
	}
	// Selection, in a stable order (oldest first), so file name collisions resolve the same way
	// on every run.
	var plan []*planned
	for i := range all {
		n := &all[i]
		m, mapped := maps[n.ID]
		p := &planned{note: n, docID: DocumentID(*n), status: "pending"}
		switch {
		case strings.TrimSpace(n.Content) == "":
			p.status = "skipped"
			p.detail = "vacía"
		case m.Skip:
			p.status = "skipped"
			p.detail = "marcada skip en la planilla"
		case o.OnlyMapped && !mapped:
			p.status = "skipped"
			p.detail = "sin mapeo (--only-mapped)"
		case !o.IncludeAnonymous && n.Owner == "" && !mapped:
			p.status = "skipped"
			p.detail = "anónima (usar --include-anonymous o mapearla)"
		}
		p.folder = m.Folder
		if p.folder == "" {
			p.folder = DefaultFolder
		}
		p.fileName = m.FileName
		plan = append(plan, p)
	}
	selected := 0
	for _, p := range plan {
		if p.status == "pending" {
			selected++
			if o.Limit > 0 && selected > o.Limit {
				p.status, p.detail = "skipped", "fuera del --limit"
			}
		}
	}
	importedLink := map[string]string{}
	for _, p := range plan {
		if p.status == "pending" {
			importedLink[p.note.ID] = webURL + "/docs/" + p.docID
		}
	}
	// File names: unique per folder, stable across runs.
	taken := map[string]map[string]bool{}
	existingNames := map[string]string{}
	if svc != nil {
		existingNames, err = existingDocNames(ctx, svc, plan)
		if err != nil {
			return err
		}
		for _, p := range plan {
			if name, ok := existingNames[p.docID]; ok {
				if taken[p.folder] == nil {
					taken[p.folder] = map[string]bool{}
				}
				taken[p.folder][strings.ToLower(name)] = true
			}
		}
	}
	for _, p := range plan {
		if p.status != "pending" {
			continue
		}
		if taken[p.folder] == nil {
			taken[p.folder] = map[string]bool{}
		}
		if name, ok := existingNames[p.docID]; ok {
			p.fileName = name
		} else {
			if p.fileName == "" {
				p.fileName = FileName(p.note.Title, "sin-titulo-"+p.note.ShortID)
			}
			p.fileName = UniqueName(p.fileName, taken[p.folder])
		}
		taken[p.folder][strings.ToLower(p.fileName)] = true

		content, warnings := BuildContent(*p.note, p.note.Content)
		p.warnings = warnings
		content, p.unresolved = RewriteNoteLinks(content, o.HedgeDocURL, resolver, func(n *Note) string { return importedLink[n.ID] })
		p.assets = AssetURLs(content, o.AssetPrefixes)
		for _, u := range p.assets {
			content = strings.ReplaceAll(content, u, webURL+"/docs/attachments/"+AttachmentID(*p.note, u))
		}
		p.content = content
		p.findings = secrets.Scan(p.note.Content)
	}

	if o.DryRun || svc == nil {
		for _, p := range plan {
			if p.status == "pending" {
				p.status = "planned"
			}
		}
		return finish(o, plan, log, true)
	}
	if err := execute(ctx, o, svc, plan, log); err != nil {
		_ = finish(o, plan, log, false)
		return err
	}
	return finish(o, plan, log, false)
}

func existingDocNames(ctx context.Context, svc *service.Service, plan []*planned) (map[string]string, error) {
	ids := make([]string, 0, len(plan))
	for _, p := range plan {
		ids = append(ids, p.docID)
	}
	out := map[string]string{}
	rows, err := svc.DB.Query(ctx, `SELECT id, file_name FROM documents WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func execute(ctx context.Context, o Options, svc *service.Service, plan []*planned, log *slog.Logger) error {
	call := &service.Call{Caller: svc.SelfCaller(), CorrelationID: "hedgedoc-import-" + protocol.NewULID()}
	folders := map[string]string{"/": protocol.RootFolderID}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	for _, p := range plan {
		if p.status != "pending" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		folderID, err := ensureFolder(ctx, svc, call, folders, p.folder)
		if err != nil {
			return fmt.Errorf("folder %s: %w", p.folder, err)
		}
		_, err = svc.CreateDocument(ctx, call, service.NewDocument{
			ID: p.docID, FolderID: folderID, FileName: p.fileName, Content: []byte(p.content), Via: "service",
			Message: Message(*p.note), CreatedAt: p.note.CreatedAt, UpdatedAt: p.note.UpdatedAt,
		})
		switch {
		case err == nil:
			p.status = "imported"
		case protocol.CodeOf(err) == protocol.CodeAlreadyExists:
			p.status, p.detail = "exists", "ya existe con otro contenido (modificada después de importar o nombre repetido)"
			continue
		default:
			p.status, p.detail = "failed", protocol.CodeOf(err)
			log.Warn("import: note failed", "note", p.note.ID, "errorCode", protocol.CodeOf(err))
			continue
		}
		for _, u := range p.assets {
			if err := migrateAsset(ctx, svc, call, hc, o.MaxAssetBytes, p, u); err != nil {
				p.warnings = append(p.warnings, "adjunto no migrado: "+AssetName(u)+": "+err.Error())
				log.Warn("import: asset not migrated", "note", p.note.ID, "asset", AssetName(u), "err", err.Error())
			}
		}
		log.Info("import: note imported", "note", p.note.ID, "document", p.docID, "assets", len(p.assets))
	}
	return nil
}

// ensureFolder creates the folders of a path (as the service). /importado/ is created closed:
// no inheritance and a single rule, manage for the admin role.
func ensureFolder(ctx context.Context, svc *service.Service, call *service.Call, cache map[string]string, p string) (string, error) {
	if id, ok := cache[p]; ok {
		return id, nil
	}
	tree, err := svc.Tree(ctx)
	if err != nil {
		return "", err
	}
	if f, ok := tree.ByPath(p); ok {
		cache[p] = f.ID
		return f.ID, nil
	}
	parentPath := path.Dir(strings.TrimSuffix(p, "/"))
	if !strings.HasSuffix(parentPath, "/") {
		parentPath += "/"
	}
	parentID, err := ensureFolder(ctx, svc, call, cache, parentPath)
	if err != nil {
		return "", err
	}
	name := path.Base(strings.TrimSuffix(p, "/"))
	closed := p == DefaultFolder
	out, err := svc.CreateFolder(ctx, call, FolderID(p), parentID, name, !closed, false, false)
	if err != nil {
		return "", err
	}
	id := out["id"].(string)
	if closed {
		if _, err := svc.ReplaceACL(ctx, call, id, []acl.Rule{{PrincipalType: "role", Principal: "admin", Permission: "manage"}}); err != nil {
			return "", err
		}
	}
	cache[p] = id
	return id, nil
}

func migrateAsset(ctx context.Context, svc *service.Service, call *service.Call, hc *http.Client, max int64, p *planned, u string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("descarga: %w", err)
	}
	data, err := readAll(resp.Body, max)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("descarga: HTTP %d", resp.StatusCode)
	}
	name := AssetName(u)
	mimeType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
		mimeType, _, _ = mime.ParseMediaType(byExt)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	sum := sha256.Sum256(data)
	attID := AttachmentID(*p.note, u)
	out, err := svc.RequestAttachmentUpload(ctx, call, service.AttachmentUpload{
		ID: attID, DocumentID: p.docID, FileName: name, MimeType: mimeType, Size: int64(len(data)),
		Checksum: "sha256:" + hex.EncodeToString(sum[:]),
	})
	if protocol.CodeOf(err) == protocol.CodeAlreadyExists {
		return nil // migrated by a previous run
	}
	if err != nil {
		return fmt.Errorf("%s", protocol.CodeOf(err))
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, out["uploadUrl"].(string), strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	put.ContentLength = int64(len(data))
	put.Header.Set("Content-Type", mimeType)
	presp, err := hc.Do(put)
	if err != nil {
		return fmt.Errorf("subida: %w", err)
	}
	_, _ = io.Copy(io.Discard, presp.Body)
	_ = presp.Body.Close()
	if presp.StatusCode/100 != 2 {
		return fmt.Errorf("subida: HTTP %d", presp.StatusCode)
	}
	if _, err := svc.ConfirmAttachment(ctx, call, attID); err != nil {
		return fmt.Errorf("confirmación: %s", protocol.CodeOf(err))
	}
	return nil
}

func exportMapping(file string, notes []Note, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"note", "shortid", "alias", "title", "owner", "createdAt", "updatedAt", "bytes", "permission", "folder", "fileName", "skip"})
	n := 0
	for _, note := range notes {
		if strings.TrimSpace(note.Content) == "" {
			continue
		}
		n++
		_ = w.Write([]string{note.ID, note.ShortID, note.Alias, note.Title, note.Owner,
			note.CreatedAt.Format(time.RFC3339), note.UpdatedAt.Format(time.RFC3339), fmt.Sprint(len(note.Content)),
			note.Permission, "", "", ""})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	log.Info("mapping sheet written", "file", file, "notes", n)
	return nil
}

func finish(o Options, plan []*planned, log *slog.Logger, dry bool) error {
	counts := map[string]int{}
	secretNotes, assets, unresolved := 0, 0, 0
	for _, p := range plan {
		counts[p.status]++
		if p.status == "skipped" {
			continue
		}
		if len(p.findings) > 0 {
			secretNotes++
		}
		assets += len(p.assets)
		unresolved += len(p.unresolved)
	}
	if o.Report != "" {
		if err := writeReport(o.Report, plan); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := []any{"dryRun", dry, "notes", len(plan), "possibleSecrets", secretNotes, "assets", assets,
		"unresolvedLinks", unresolved, "report", o.Report}
	for _, k := range keys {
		attrs = append(attrs, k, counts[k])
	}
	log.Info("hedgedoc import finished", attrs...)
	if secretNotes > 0 {
		log.Warn("notes with possible secrets: they were public in HedgeDoc, rotate those credentials before shutting it down",
			"notes", secretNotes, "report", o.Report)
	}
	return nil
}

func writeReport(file string, plan []*planned) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"note", "shortid", "title", "status", "detail", "documentId", "path", "assets", "possibleSecrets", "unresolvedLinks", "warnings"})
	for _, p := range plan {
		var rules []string
		for _, fd := range p.findings {
			rules = append(rules, fmt.Sprintf("%s:%d", fd.Rule, fd.Line))
		}
		docPath := ""
		if p.status != "skipped" {
			docPath = p.folder + p.fileName
		}
		_ = w.Write([]string{p.note.ID, p.note.ShortID, p.note.Title, p.status, p.detail, p.docID, docPath,
			fmt.Sprint(len(p.assets)), strings.Join(rules, " "), strings.Join(p.unresolved, " "), strings.Join(p.warnings, " | ")})
	}
	w.Flush()
	return w.Error()
}
