// Package secrets is the credential detector. Documents must not hold secrets; when one looks
// like it does, the write still succeeds but the reply carries `possible_secret_detected` and
// the document is flagged (sharing it then requires an explicit acknowledgement).
//
// Findings never carry the matched text: only the rule and the line.
package secrets

import (
	"regexp"
	"sort"
	"strings"
)

// Finding is one match: which rule and on which line (1-based).
type Finding struct {
	Rule string `json:"rule"`
	Line int    `json:"line"`
}

type rule struct {
	name string
	re   *regexp.Regexp
}

var rules = []rule{
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"aws_access_key_id", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[0-9A-Z]{16}\b`)},
	{"aws_secret_access_key", regexp.MustCompile(`(?i)aws_?secret_?access_?key["'\s]*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{"github_token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})\b`)},
	{"gitlab_token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack_token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"slack_webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/]{20,}`)},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"stripe_key", regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{20,}\b`)},
	{"anthropic_key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai_key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}`)},
	{"digitalocean_token", regexp.MustCompile(`\bdo[opr]_v1_[a-f0-9]{64}\b`)},
	{"sendgrid_key", regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"jiku_docs_share_token", regexp.MustCompile(`\bjds_[A-Za-z0-9_-]{43}\b`)},
	{"url_with_password", regexp.MustCompile(`\b[a-z][a-z0-9+.-]{1,20}://[^\s:@/'"<>]+:[^\s@/'"<>]{3,}@[^\s/'"<>]+`)},
	{"password_assignment", regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|contrase(?:ñ|n)a|clave|passphrase|secret|token|api[_-]?key)\b\s*[:=]\s*["'` + "`" + `]?[^\s"'` + "`" + `]{6,}`)},
}

// placeholder values that are documentation, not secrets.
var placeholderRe = regexp.MustCompile(`(?i)(?:[:=]\s*["'` + "`" + `]?)(?:\*{3,}|x{4,}|<[^>]+>|\$\{[^}]+\}|\{\{[^}]+\}\}|changeme|example|ejemplo|placeholder|your[_-]|tu[_-]|redacted|\.\.\.)`)

// Scan returns the findings of a text, at most one per rule and line, sorted by line.
func Scan(text string) []Finding {
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		if len(line) > 4000 {
			line = line[:4000]
		}
		for _, r := range rules {
			loc := r.re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if r.name == "password_assignment" || r.name == "url_with_password" {
				if placeholderRe.MatchString(line[loc[0]:loc[1]]) {
					continue
				}
			}
			out = append(out, Finding{Rule: r.name, Line: i + 1})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// Rules lists the rule names, for documentation and reports.
func Rules() []string {
	names := make([]string, len(rules))
	for i, r := range rules {
		names[i] = r.name
	}
	return names
}
