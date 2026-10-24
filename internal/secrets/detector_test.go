package secrets

import "testing"

func TestScan(t *testing.T) {
	// Synthetic values only: none of these is a real credential.
	text := "línea limpia\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"aws: AKIAABCDEFGHIJKLMNOP\n" +
		"contraseña: Sup3rS3cret!\n" +
		"password: <tu-contraseña>\n" +
		"postgres://app:hunter22@db.internal/prod\n" +
		"token ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB\n"
	got := Scan(text)
	want := map[int]string{2: "private_key", 3: "aws_access_key_id", 4: "password_assignment", 6: "url_with_password", 7: "github_token"}
	found := map[int]bool{}
	for _, f := range got {
		if want[f.Line] == f.Rule {
			found[f.Line] = true
		}
		if f.Line == 1 || f.Line == 5 {
			t.Errorf("false positive %+v", f)
		}
	}
	for line, rule := range want {
		if !found[line] {
			t.Errorf("missing %s on line %d (got %+v)", rule, line, got)
		}
	}
}
