package service

import "github.com/gravadigital/jiku-docs/internal/query"

// The resource sheets. The validator and meta.describe read these same structures, and the
// contract tests compare them with docs/apis/jiku-docs-queries.yaml.

// visibilityEnum: restricted is read only by admins and services (P-1). As a filter, a caller
// that cannot read restricted documents gets an empty result, never an error.
var visibilityEnum = []string{"internal", "public", "restricted"}

var foldersSpec = &query.Spec{
	Name:       "folders",
	Base:       []string{"id", "name", "path", "parentId", "inheritAcl", "externalBlocked", "sharingBlocked", "createdAt", "createdBy"},
	Fields:     []string{"id", "name", "path", "parentId", "inheritAcl", "externalBlocked", "sharingBlocked", "createdAt", "createdBy"},
	Includable: []string{"effectivePermission"},
	IncludableSpecs: map[string]query.FieldSpec{
		"effectivePermission": {Kind: "computed"},
	},
	Filterable: []string{"id", "parentId", "path", "pathPrefix"},
	FilterableSpecs: map[string]query.FilterSpec{
		"id":         {Kind: query.KindString, ULID: true, List: true},
		"path":       {Kind: query.KindString},
		"parentId":   {Kind: query.KindString, ULID: true},
		"pathPrefix": {Kind: query.KindString, Prefix: true},
	},
	Sortable:    []string{"path", "name", "createdAt"},
	DefaultSort: []string{"path"},
	Enums:       map[string][]string{"permission": {"read", "write", "manage"}},
	Levers:      []string{"filter", "sort", "page", "fields", "include", "count"},
}

// folderGetIncludes are the names folders.get accepts in `include`. `acl` (obsolete since P-1:
// the rules have no effect) only for admins and services.
var folderGetIncludes = []string{"acl", "effectivePermission"}

var documentFilterable = []string{"id", "folderId", "path", "pathPrefix", "clientId", "projectId", "requirementId", "taskId",
	"topic", "visibility", "createdBy", "updatedBy", "createdAt", "updatedAt", "fileName", "deleted",
	"hasProject", "hasClient", "hasTopic"}

var documentFilterSpecs = map[string]query.FilterSpec{
	"id":            {Kind: query.KindString, ULID: true, List: true},
	"folderId":      {Kind: query.KindString, ULID: true},
	"path":          {Kind: query.KindString},
	"pathPrefix":    {Kind: query.KindString, Prefix: true},
	"clientId":      {Kind: query.KindInteger, List: true},
	"projectId":     {Kind: query.KindInteger, List: true},
	"requirementId": {Kind: query.KindInteger, List: true},
	"taskId":        {Kind: query.KindInteger, List: true},
	"topic":         {Kind: query.KindString, List: true},
	"visibility":    {Kind: query.KindString, Enum: "visibility"},
	"createdBy":     {Kind: query.KindString, List: true},
	"updatedBy":     {Kind: query.KindString, List: true},
	"createdAt":     {Kind: query.KindDate, Range: true},
	"updatedAt":     {Kind: query.KindDate, Range: true},
	"fileName":      {Kind: query.KindString, Prefix: true},
	"deleted":       {Kind: query.KindBoolean},
	// P-1: false is "Sin proyecto" (neither direct nor through a requirement or a task), "Sin
	// cliente" (no jiku.clients) and "Sin tema".
	"hasProject": {Kind: query.KindBoolean},
	"hasClient":  {Kind: query.KindBoolean},
	"hasTopic":   {Kind: query.KindBoolean},
}

// documentFields: clients and projects are the direct relations (jiku.clients, jiku.projects)
// as ids, for "Cliente · Proyecto"; the api resolves the names with core (P-1).
var documentFields = []string{"id", "title", "path", "folderId", "fileName", "version", "checksum", "sizeBytes",
	"visibility", "createdAt", "createdBy", "createdByName", "updatedAt", "updatedBy", "updatedByName", "deletedAt",
	"clients", "projects"}

var documentsSpec = &query.Spec{
	Name: "documents",
	Base: []string{"id", "title", "path", "folderId", "version", "visibility", "updatedAt", "updatedBy", "updatedByName",
		"clients", "projects"},
	Fields:     documentFields,
	Includable: []string{"relations", "topics", "lock"},
	IncludableSpecs: map[string]query.FieldSpec{
		"relations": {Kind: "computed", Fields: []string{"clients", "projects", "requirements", "tasks"}},
		"topics":    {Kind: "computed"},
		"lock":      {Kind: "relation", Cardinality: "one", Fields: []string{"sessionId", "holder", "holderName", "via", "expiresAt"}, Optional: true},
	},
	Filterable:      documentFilterable,
	FilterableSpecs: documentFilterSpecs,
	AllowOr:         true,
	Sortable:        []string{"updatedAt", "createdAt", "title", "path"},
	DefaultSort:     []string{"-updatedAt"},
	Enums:           map[string][]string{"visibility": visibilityEnum},
	Levers:          []string{"filter", "sort", "page", "fields", "include", "count"},
}

// documents.get also returns prunedVersionsCount: how many versions the history compaction
// removed (docs/compactacion-historial-v1.md). Never to an external person.
var documentGetFields = append(append([]string{}, documentFields...), "prunedVersionsCount")

// documents.get accepts more includes than the list.
var documentGetIncludes = []string{"content", "relations", "topics", "lock", "attachments"}

// searchSpec reuses the document filters (documents.search accepts the same filters).
var searchSpec = &query.Spec{
	Name: "documents.search", Filterable: documentFilterable, FilterableSpecs: documentFilterSpecs, AllowOr: true,
	Enums:  map[string][]string{"visibility": visibilityEnum, "mode": {"text", "semantic", "hybrid"}},
	Levers: []string{"filter"},
}

var versionsSpec = &query.Spec{
	Name:       "documents.versions",
	Base:       []string{"version", "checksum", "sizeBytes", "author", "authorName", "via", "sessionId", "message", "createdAt", "kind", "protected"},
	Fields:     []string{"version", "checksum", "sizeBytes", "author", "authorName", "via", "sessionId", "message", "createdAt", "kind", "protected"},
	Filterable: []string{"documentId", "author", "via", "createdAt", "kind", "protected"},
	FilterableSpecs: map[string]query.FilterSpec{
		"documentId": {Kind: query.KindString, ULID: true, Required: true},
		"author":     {Kind: query.KindString, List: true},
		"via":        {Kind: query.KindString, Enum: "via"},
		"createdAt":  {Kind: query.KindDate, Range: true},
		"kind":       {Kind: query.KindString, Enum: "kind"},
		"protected":  {Kind: query.KindBoolean},
	},
	Sortable:    []string{"version"},
	DefaultSort: []string{"-version"},
	Enums:       map[string][]string{"via": {"human", "agent", "service"}, "kind": {KindCreate, KindUpdate, KindRestore}},
	Levers:      []string{"filter", "sort", "page"},
	Key:         "version",
}

var changesSpec = &query.Spec{
	Name: "documents.changes", Filterable: []string{"pathPrefix"},
	FilterableSpecs: map[string]query.FilterSpec{"pathPrefix": {Kind: query.KindString, Prefix: true}},
	Levers:          []string{"filter"},
}

var topicsSpec = &query.Spec{
	Name:            "topics",
	Base:            []string{"topic", "documents"},
	Fields:          []string{"topic", "documents"},
	Filterable:      []string{"prefix"},
	FilterableSpecs: map[string]query.FilterSpec{"prefix": {Kind: query.KindString, Prefix: true}},
	Sortable:        []string{},
	DefaultSort:     []string{"topic"},
	Levers:          []string{"filter", "page"},
	Key:             "topic",
}

var attachmentsSpec = &query.Spec{
	Name:            "attachments",
	Base:            []string{"id", "documentId", "fileName", "mimeType", "sizeBytes", "link", "uploadedBy", "createdAt"},
	Fields:          []string{"id", "documentId", "fileName", "mimeType", "sizeBytes", "link", "uploadedBy", "createdAt"},
	Filterable:      []string{"documentId"},
	FilterableSpecs: map[string]query.FilterSpec{"documentId": {Kind: query.KindString, ULID: true, Required: true}},
	DefaultSort:     []string{"createdAt"},
	Levers:          []string{"filter"},
}

var sharesSpec = &query.Spec{
	Name: "shares",
	Base: []string{"id", "documentId", "mode", "version", "includeAttachments", "note", "status", "createdBy", "createdAt",
		"expiresAt", "revokedAt", "revokedBy", "revokeReason", "accessCount", "lastAccessAt"},
	Fields: []string{"id", "documentId", "mode", "version", "includeAttachments", "note", "status", "createdBy", "createdAt",
		"expiresAt", "revokedAt", "revokedBy", "revokeReason", "accessCount", "lastAccessAt"},
	Filterable: []string{"documentId", "status", "createdBy"},
	FilterableSpecs: map[string]query.FilterSpec{
		"documentId": {Kind: query.KindString, ULID: true, Required: true},
		"status":     {Kind: query.KindString, Enum: "status"},
		"createdBy":  {Kind: query.KindString, List: true},
	},
	Sortable:    []string{"createdAt", "expiresAt"},
	DefaultSort: []string{"-createdAt"},
	Enums: map[string][]string{"status": {"active", "revoked", "expired"}, "mode": {"latest", "pinned"},
		"revokeReason": {"manual", "document_deleted", "restricted"}},
	Levers: []string{"filter", "sort", "page"},
}

// describedSpecs are the resources meta.describe returns, keyed by the method prefix
// (`{resource}.list`), so a generic client can call the list of any of them.
var describedSpecs = []*query.Spec{foldersSpec, documentsSpec, versionsSpec, topicsSpec, attachmentsSpec, sharesSpec}

// Specs exposes the sheets to the contract tests.
func Specs() []*query.Spec { return describedSpecs }

// SearchSpec and ChangesSpec expose the two non-list sheets to the contract tests.
func SearchSpec() *query.Spec  { return searchSpec }
func ChangesSpec() *query.Spec { return changesSpec }
