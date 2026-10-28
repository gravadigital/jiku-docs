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
	"strconv"
	"strings"
	"time"

	"github.com/gravadigital/jiku-docs/internal/acl"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/markdown"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/secrets"
	"github.com/gravadigital/jiku-docs/internal/service"
)

// DefaultFolder receives the notes without a mapping. They are imported with visibility
// restricted (P-1): only administrators read them.
const DefaultFolder = "/imported/"

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
	// CheckRelations: in a dry run, validate the relations against core (Relations, or the core of
	// the service) and report the ones that would be discarded.
	CheckRelations bool
	// Relations validates relation ids in a dry run (--check-relations).
	Relations RelationChecker
}

// RelationChecker resolves which ids of an entity type exist and are visible (core's Entities).
type RelationChecker interface {
	Entities(ctx context.Context, entityType string, ids []int64) (map[int64]coreapi.Entity, error)
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
		"DSN of the MariaDB database loaded with the dump (go-sql-driver/mysql, with parseTime=true)")
	fs.StringVar(&o.Mapping, "mapping", "", "CSV mapping sheet note -> folder (columns: note, folder[, fileName][, skip]"+
		"[, clients, projects, requirements, tasks: ids separated by ; or ,][, topics: separated by ;])")
	fs.StringVar(&o.Report, "report", ".local/hedgedoc-import-report.csv", "CSV report of the run (status and possible secrets per note)")
	fs.StringVar(&o.ExportMapping, "export-mapping", "", "writes a mapping sheet to fill in with every note and exits")
	fs.BoolVar(&o.DryRun, "dry-run", false, "computes the plan and the report without writing anything or downloading images")
	fs.BoolVar(&o.OnlyMapped, "only-mapped", false, "imports only the notes listed in the mapping sheet")
	fs.BoolVar(&o.IncludeAnonymous, "include-anonymous", false, "includes the notes without an owner (created without a session; in the real dump almost all are spam)")
	fs.BoolVar(&o.CheckRelations, "check-relations", false, "with --dry-run: validates the relations against core over the bus (read only) and reports the ones that would be dropped")
	fs.IntVar(&o.Limit, "limit", 0, "imports at most N notes (for testing)")
	fs.StringVar(&o.HedgeDocURL, "hedgedoc-url", "https://docs.grava.io", "base URL of HedgeDoc, to rewrite links between notes")
	fs.Var(&prefixes, "asset-prefix", "URL prefix (without scheme) of the images to migrate as attachments; repeatable")
	fs.Int64Var(&o.MaxAssetBytes, "max-asset-bytes", 50<<20, "maximum size of an attachment")
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
	// Decided: the row has a folder, a fileName or skip. Relations and topics alone do not select
	// a note for import (they only apply to the notes that are imported).
	Decided   bool
	Relations markdown.Relations
	Topics    []string
}

// parseIDs reads a list of Jiku ids separated by `;` or `,`.
func parseIDs(s string) ([]int64, error) {
	var out []int64
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q is not a valid numeric id", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// parseTopics reads a list of topics separated by `;`.
func parseTopics(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ";") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
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
		return nil, errors.New("mapping: the note column is missing")
	}
	folderCol, ok := col["folder"]
	if !ok {
		return nil, errors.New("mapping: the folder column is missing")
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
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("mapping line %d: %w", line, err)
		}
		if noteCol >= len(rec) || strings.TrimSpace(rec[noteCol]) == "" {
			continue
		}
		n := r.Find(strings.TrimSpace(rec[noteCol]))
		if n == nil {
			return nil, fmt.Errorf("mapping line %d: the note %q does not exist", line, rec[noteCol])
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
			return nil, fmt.Errorf("mapping line %d: invalid fileName %q (it must end in .md)", line, m.FileName)
		}
		for _, k := range markdown.RelationKinds {
			ids, err := parseIDs(get(rec, k.Key))
			if err != nil {
				return nil, fmt.Errorf("mapping line %d: column %s: %w", line, k.Key, err)
			}
			m.Relations.Set(k.Key, ids)
		}
		m.Relations = m.Relations.Normalize()
		if m.Topics, err = NormalizeTopics(parseTopics(get(rec, "topics"))); err != nil {
			return nil, fmt.Errorf("mapping line %d: column topics: %w", line, err)
		}
		// A row left blank (as --export-mapping writes every note) is not a decision: it must not
		// count as mapped, or the ~10.000 anonymous spam notes of the exported sheet would be imported.
		m.Decided = m.Folder != "" || m.FileName != "" || m.Skip
		if !m.Decided && m.Relations.Empty() && len(m.Topics) == 0 {
			continue
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
	// base is the content before the relations and topics of the sheet; content = base + them.
	base   string
	rel    markdown.Relations
	topics []string
	// drop are the relations discarded (frontmatter key -> ids), dropped says which and why.
	drop    map[string][]int64
	dropped []string
}

// render rebuilds the content from base, the relations and topics of the sheet and the dropped
// relations (which are also removed when they came from the note's own frontmatter).
func (p *planned) render() error {
	content, err := MergeFrontmatter(p.base, p.rel, p.topics)
	if err != nil {
		return err
	}
	if len(p.drop) > 0 {
		if content, err = markdown.RemoveRelations(content, p.drop); err != nil {
			return err
		}
	}
	p.content = content
	return nil
}

// dropRelation discards one relation; false when it was already discarded or is unknown.
func (p *planned) dropRelation(entityType string, id int64, reason string) bool {
	key := ""
	for _, k := range markdown.RelationKinds {
		if k.EntityType == entityType {
			key = k.Key
		}
	}
	if key == "" {
		return false
	}
	for _, v := range p.drop[key] {
		if v == id {
			return false
		}
	}
	if p.drop == nil {
		p.drop = map[string][]int64{}
	}
	p.drop[key] = append(p.drop[key], id)
	p.rel, _ = WithoutRelation(p.rel, entityType, id)
	p.dropped = append(p.dropped, fmt.Sprintf("%s %d: %s", entityType, id, reason))
	return true
}

// frontmatter is the frontmatter the document will have (zero value if it does not parse).
func (p *planned) frontmatter() markdown.Frontmatter {
	fm, _, _ := markdown.Parse(p.content)
	return fm
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
		m, ok := maps[n.ID]
		mapped := ok && m.Decided
		p := &planned{note: n, docID: DocumentID(*n), status: "pending"}
		switch {
		case strings.TrimSpace(n.Content) == "":
			p.status = "skipped"
			p.detail = "empty"
		case m.Skip:
			p.status = "skipped"
			p.detail = "marked skip in the mapping sheet"
		case o.OnlyMapped && !mapped:
			p.status = "skipped"
			p.detail = "not mapped (--only-mapped)"
		case !o.IncludeAnonymous && n.Owner == "" && !mapped:
			p.status = "skipped"
			p.detail = "anonymous (use --include-anonymous or map it)"
		}
		p.folder = m.Folder
		if p.folder == "" {
			p.folder = DefaultFolder
		}
		p.fileName = m.FileName
		p.rel, p.topics = m.Relations, m.Topics
		plan = append(plan, p)
	}
	selected := 0
	for _, p := range plan {
		if p.status == "pending" {
			selected++
			if o.Limit > 0 && selected > o.Limit {
				p.status, p.detail = "skipped", "beyond --limit"
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
				p.fileName = FileName(p.note.Title, "untitled-"+p.note.ShortID)
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
		if p.folder == DefaultFolder {
			// P-1: the folder no longer hides anything; the notes nobody classified stay readable
			// only by admins, as /imported/ was.
			if c, err := markdown.SetVisibility(content, markdown.VisibilityRestricted); err == nil {
				content = c
			}
		}
		p.base = content
		if err := p.render(); err != nil {
			p.warnings = append(p.warnings, "relations and topics of the mapping sheet not applied: "+err.Error())
			p.rel, p.topics, p.content = markdown.Relations{}, nil, content
		}
		p.findings = secrets.Scan(p.note.Content)
	}

	if o.DryRun || svc == nil {
		checker := o.Relations
		if checker == nil && svc != nil {
			checker = svc.Core
		}
		if o.CheckRelations {
			if checker == nil {
				return errors.New("--check-relations needs a connection to core")
			}
			if err := checkRelations(ctx, checker, plan); err != nil {
				return fmt.Errorf("validating relations against core: %w", err)
			}
		}
		for _, p := range plan {
			if p.status == "pending" {
				p.status = "planned"
			}
		}
		return finish(o, plan, log, true)
	}
	if err := execute(ctx, o, svc, plan, existingNames, log); err != nil {
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

func execute(ctx context.Context, o Options, svc *service.Service, plan []*planned, existing map[string]string, log *slog.Logger) error {
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
		writeNote(ctx, svc, call, p, folderID, existing, log)
		if p.status != "imported" && p.status != "updated" && p.status != "unchanged" {
			continue
		}
		for _, u := range p.assets {
			// A rerun must not download again what a previous run already migrated.
			if ready, err := attachmentReady(ctx, svc, AttachmentID(*p.note, u)); err != nil {
				return err
			} else if ready {
				continue
			}
			if err := migrateAsset(ctx, svc, call, hc, o.MaxAssetBytes, p, u); err != nil {
				p.warnings = append(p.warnings, "attachment not migrated: "+AssetName(u)+": "+err.Error())
				log.Warn("import: asset not migrated", "note", p.note.ID, "asset", AssetName(u), "err", err.Error())
			}
		}
		if p.status == "imported" || p.status == "updated" {
			log.Info("import: note "+p.status, "note", p.note.ID, "document", p.docID, "assets", len(p.assets))
		}
	}
	return nil
}

// relationReason is what the report says about a relation core rejected.
const relationReason = "invalid_relation: it does not exist in Jiku or jiku-docs cannot see it"

// writeNote creates the document (or updates a previous import of it) and, when core rejects a
// relation (invalid_relation), retries without it: the note is imported with the rest and the
// report says which relation was discarded and why. Failures end up in p.status.
func writeNote(ctx context.Context, svc *service.Service, call *service.Call, p *planned, folderID string, existing map[string]string, log *slog.Logger) {
	_, already := existing[p.docID]
	for {
		var err error
		if already {
			err = reimport(ctx, svc, call, p, folderID)
		} else {
			_, err = svc.CreateDocument(ctx, call, service.NewDocument{
				ID: p.docID, FolderID: folderID, FileName: p.fileName, Content: []byte(p.content), Via: "service",
				Message: Message(*p.note), CreatedAt: p.note.CreatedAt, UpdatedAt: p.note.UpdatedAt,
			})
			if err == nil {
				p.status = "imported"
			}
		}
		if err == nil {
			return
		}
		code := protocol.CodeOf(err)
		if code == protocol.CodeInvalidRelation {
			if et, id, ok := relationOf(err); ok && p.dropRelation(et, id, relationReason) && p.render() == nil {
				log.Warn("import: relation discarded, retrying without it", "note", p.note.ID, "entityType", et, "entityId", id)
				continue
			}
		}
		if code == protocol.CodeAlreadyExists && !already {
			p.status, p.detail = "exists", "already exists with other content (created during the run or repeated name)"
			return
		}
		p.status, p.detail = "failed", code
		log.Warn("import: note failed", "note", p.note.ID, "errorCode", code)
		return
	}
}

// relationOf reads errorDetails.entityType and entityId of an invalid_relation.
func relationOf(err error) (string, int64, bool) {
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		return "", 0, false
	}
	et, _ := pe.Details["entityType"].(string)
	var id int64
	switch v := pe.Details["entityId"].(type) {
	case int64:
		id = v
	case int:
		id = int64(v)
	case float64:
		id = int64(v)
	case string:
		id, _ = strconv.ParseInt(v, 10, 64)
	}
	return et, id, et != "" && id > 0
}

// reimport handles a note a previous run already imported. Same content: unchanged. Different
// content (for example, relations or topics added to the sheet): a new version with
// documents.{id}.update on the current version, but only while every version since the import
// is the importer's own; a document edited, moved, renamed or deleted in jiku-docs is not
// overwritten.
func reimport(ctx context.Context, svc *service.Service, call *service.Call, p *planned, folderID string) error {
	var curFolder, curName, checksum, via, message string
	var version int
	var deleted bool
	err := svc.DB.QueryRow(ctx, `SELECT d.folder_id, d.file_name, d.current_version, d.checksum, d.deleted_at IS NOT NULL,
		v.via, COALESCE(v.message, '')
		FROM documents d JOIN document_versions v ON v.document_id = d.id AND v.version = d.current_version
		WHERE d.id = $1`, p.docID).Scan(&curFolder, &curName, &version, &checksum, &deleted, &via, &message)
	if err != nil {
		return err
	}
	switch {
	case deleted:
		p.status, p.detail = "exists", "deleted in jiku-docs after the import: not restored"
	case curFolder != folderID || !strings.EqualFold(curName, p.fileName):
		p.status, p.detail = "exists", "moved or renamed in jiku-docs after the import: not overwritten"
	case checksum == service.Checksum([]byte(p.content)):
		p.status, p.detail = "unchanged", "already imported in a previous run"
	case via != "service" || !IsImportMessage(message):
		p.status, p.detail = "exists", "edited in jiku-docs after the import: not overwritten"
	default:
		out, err := svc.UpdateDocumentContent(ctx, call, p.docID, version, []byte(p.content), "service", ReimportMessage(*p.note))
		if err != nil {
			return err
		}
		p.status, p.detail = "updated", fmt.Sprintf("version %v: reimported with other content", out["version"])
	}
	return nil
}

// checkRelations validates against core the relations the documents would have (dry run) and
// discards the ones that do not exist or are not visible, as the real run would.
func checkRelations(ctx context.Context, checker RelationChecker, plan []*planned) error {
	for _, k := range markdown.RelationKinds {
		seen := map[int64]bool{}
		var ids []int64
		for _, p := range plan {
			if p.status != "pending" {
				continue
			}
			fm := p.frontmatter()
			for _, id := range fm.Relations.Get(k.Key) {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			continue
		}
		found, err := checker.Entities(ctx, k.EntityType, ids)
		if err != nil {
			return err
		}
		for _, p := range plan {
			if p.status != "pending" {
				continue
			}
			changed := false
			fm := p.frontmatter()
			for _, id := range fm.Relations.Get(k.Key) {
				if _, ok := found[id]; !ok && p.dropRelation(k.EntityType, id, relationReason) {
					changed = true
				}
			}
			if changed {
				if err := p.render(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// attachmentReady reports whether an attachment id was already migrated and confirmed.
func attachmentReady(ctx context.Context, svc *service.Service, id string) (bool, error) {
	var ready bool
	err := svc.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attachments WHERE id = $1 AND status = 'ready')`, id).Scan(&ready)
	return ready, err
}

// ensureFolder creates the folders of a path (as the service). /imported/ is created closed:
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
		return fmt.Errorf("download: %w", err)
	}
	data, err := readAll(resp.Body, max)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
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
		return fmt.Errorf("upload: %w", err)
	}
	_, _ = io.Copy(io.Discard, presp.Body)
	_ = presp.Body.Close()
	if presp.StatusCode/100 != 2 {
		return fmt.Errorf("upload: HTTP %d", presp.StatusCode)
	}
	if _, err := svc.ConfirmAttachment(ctx, call, attID); err != nil {
		return fmt.Errorf("confirmation: %s", protocol.CodeOf(err))
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
	withRel := map[string]int{}
	withTopics, dropped := 0, 0
	for _, p := range plan {
		counts[p.status]++
		if p.status == "skipped" {
			continue
		}
		fm := p.frontmatter()
		for _, k := range markdown.RelationKinds {
			if len(fm.Relations.Get(k.Key)) > 0 {
				withRel[k.Key]++
			}
		}
		if len(fm.Topics) > 0 {
			withTopics++
		}
		dropped += len(p.dropped)
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
		"unresolvedLinks", unresolved, "report", o.Report,
		"withProjects", withRel["projects"], "withClients", withRel["clients"], "withRequirements", withRel["requirements"],
		"withTasks", withRel["tasks"], "withTopics", withTopics, "droppedRelations", dropped}
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
	_ = w.Write([]string{"note", "shortid", "title", "status", "detail", "documentId", "path", "assets", "possibleSecrets", "unresolvedLinks", "warnings",
		"clients", "projects", "requirements", "tasks", "topics", "droppedRelations"})
	for _, p := range plan {
		var rules []string
		for _, fd := range p.findings {
			rules = append(rules, fmt.Sprintf("%s:%d", fd.Rule, fd.Line))
		}
		docPath := ""
		if p.status != "skipped" {
			docPath = p.folder + p.fileName
		}
		rec := []string{p.note.ID, p.note.ShortID, p.note.Title, p.status, p.detail, p.docID, docPath,
			fmt.Sprint(len(p.assets)), strings.Join(rules, " "), strings.Join(p.unresolved, " "), strings.Join(p.warnings, " | ")}
		var fm markdown.Frontmatter
		if p.status != "skipped" {
			fm = p.frontmatter()
		}
		for _, k := range markdown.RelationKinds {
			rec = append(rec, joinIDs(fm.Relations.Get(k.Key)))
		}
		rec = append(rec, strings.Join(fm.Topics, ";"), strings.Join(p.dropped, " | "))
		_ = w.Write(rec)
	}
	w.Flush()
	return w.Error()
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ";")
}
