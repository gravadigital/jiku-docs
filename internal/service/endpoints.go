package service

// Commands are the 19 methods of `{serviceName}-commands`.
func (s *Service) Commands() []Endpoint {
	return []Endpoint{
		{Pattern: "folders.new", Handle: s.foldersNew},
		{Pattern: "folders.{folderId}.edit", Handle: s.foldersEdit},
		{Pattern: "folders.{folderId}.delete", Handle: s.foldersDelete},
		{Pattern: "folders.{folderId}.acl.replace", Handle: s.foldersACLReplace},
		{Pattern: "documents.new", Handle: s.documentsNew},
		{Pattern: "documents.{documentId}.update", Handle: s.documentsUpdate},
		{Pattern: "documents.{documentId}.move", Handle: s.documentsMove},
		{Pattern: "documents.{documentId}.delete", Handle: s.documentsDelete},
		{Pattern: "documents.{documentId}.restore", Handle: s.documentsRestore},
		{Pattern: "documents.{documentId}.lock", Handle: s.documentsLock},
		{Pattern: "documents.{documentId}.unlock", Handle: s.documentsUnlock},
		{Pattern: "documents.{documentId}.relations.update", Handle: s.documentsRelationsUpdate},
		{Pattern: "documents.request-content-upload", Handle: s.documentsRequestContentUpload},
		{Pattern: "attachments.request-upload", Handle: s.attachmentsRequestUpload},
		{Pattern: "attachments.{attachmentId}.confirm", Handle: s.attachmentsConfirm},
		{Pattern: "attachments.{attachmentId}.request-download", Handle: s.attachmentsRequestDownload},
		{Pattern: "attachments.{attachmentId}.delete", Handle: s.attachmentsDelete},
		{Pattern: "shares.new", Handle: s.sharesNew},
		{Pattern: "shares.{shareId}.revoke", Handle: s.sharesRevoke},
	}
}

// Queries are the 14 methods of `{serviceName}-queries`.
func (s *Service) Queries() []Endpoint {
	return []Endpoint{
		{Pattern: "meta.describe", Handle: s.metaDescribe},
		{Pattern: "folders.list", Handle: s.foldersList},
		{Pattern: "folders.get", Handle: s.foldersGet},
		{Pattern: "documents.list", Handle: s.documentsList},
		{Pattern: "documents.get", Handle: s.documentsGet},
		{Pattern: "documents.versions.list", Handle: s.versionsList},
		{Pattern: "documents.search", Handle: s.documentsSearch},
		{Pattern: "documents.changes", Handle: s.documentsChanges},
		{Pattern: "documents.suggest-relations", Handle: s.documentsSuggestRelations},
		{Pattern: "topics.list", Handle: s.topicsList},
		{Pattern: "attachments.list", Handle: s.attachmentsList},
		{Pattern: "shares.list", Handle: s.sharesList},
		{Pattern: "shares.resolve", APIOnly: true, Handle: s.sharesResolve},
		{Pattern: "shares.attachment", APIOnly: true, Handle: s.sharesAttachment},
	}
}
