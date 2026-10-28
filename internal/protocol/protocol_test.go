package protocol

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSubjects(t *testing.T) {
	if got := EndpointName("documents.{documentId}.update"); got != "documents-update" {
		t.Error(got)
	}
	if got := EndpointSubject("folders.{folderId}.acl.replace"); got != "folders.*.acl.replace" {
		t.Error(got)
	}
	if got := GroupSubject("dev", "jiku-docs-queries"); got != "dev.*.jiku-docs-queries.v1" {
		t.Error(got)
	}
	ps, ok := ParseSubject("prod.281234567890123456.jiku-docs-commands.v1.documents.01JBX7Q2M4N8P6R3T5V9W1Y0ZA.update")
	if !ok || ps.UserID != "281234567890123456" || ps.Method != "documents.01JBX7Q2M4N8P6R3T5V9W1Y0ZA.update" {
		t.Errorf("%+v", ps)
	}
	params, ok := MatchPattern("documents.{documentId}.update", ps.Method)
	if !ok || params["documentId"] != "01JBX7Q2M4N8P6R3T5V9W1Y0ZA" {
		t.Errorf("%v", params)
	}
	if _, ok := MatchPattern("documents.new", "documents.x.new"); ok {
		t.Error("segment count must match")
	}
	if NotifySubject("dev", "42", "document.updated") != "dev.42.docs-notify.v1.document.updated" {
		t.Error("notify subject")
	}
}

// Same algorithm as core and the auth-callout (packages/nats-protocol hashUserId).
func TestInboxPrefix(t *testing.T) {
	p := InboxPrefix("390014149763006466")
	if len(p) != len("_INBOX.")+16 || p[:7] != "_INBOX." {
		t.Errorf("%q", p)
	}
	for _, r := range p[7:] {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			t.Errorf("not lowercase base32: %q", p)
		}
	}
}

func TestReply(t *testing.T) {
	b := Failure(E(CodeVersionConflict).With("currentVersion", 8)).Encode()
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["status"] != "failure" || m["errorCode"] != "version_conflict" || m["errorMessage"] == "" {
		t.Errorf("%s", b)
	}
	if d := m["errorDetails"].(map[string]any); d["currentVersion"].(float64) != 8 {
		t.Errorf("%s", b)
	}
	b = Failure(errors.New("database password leaked here")).Encode()
	if string(b) != `{"status":"failure","errorCode":"internal_error","errorMessage":"Internal error"}` {
		t.Errorf("foreign errors must not leak: %s", b)
	}
	if !IsULID(NewULID()) || IsULID("01jbx7q2m4n8p6r3t5v9w1y0za") {
		t.Error("ulid")
	}
}
