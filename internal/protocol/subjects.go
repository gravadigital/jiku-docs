package protocol

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// Version of the command/query protocol and of the events. They are versioned separately,
// even if both are v1 today.
const (
	ProtocolVersion = "v1"
	EventsVersion   = "v1"
)

// Plane suffixes of the two micro services: `{serviceName}-commands` and `{serviceName}-queries`.
const (
	PlaneCommands = "commands"
	PlaneQueries  = "queries"
)

// ServiceFor names a micro service: jiku-docs + commands = jiku-docs-commands.
func ServiceFor(serviceName, plane string) string { return serviceName + "-" + plane }

// GroupSubject is the micro group prefix `{instance}.*.{service}.{version}`.
func GroupSubject(instance, service string) string {
	return instance + ".*." + service + "." + ProtocolVersion
}

// RequestSubject is what a caller publishes: `{instance}.{userId}.{service}.v1.{method}`.
func RequestSubject(instance, userID, service, method string) string {
	return strings.Join([]string{instance, userID, service, ProtocolVersion, method}, ".")
}

// EventSubject is the stream subject of an event type: `{instance}.docs-events.v1.{type}`.
func EventSubject(instance, eventType string) string {
	return instance + ".docs-events." + EventsVersion + "." + eventType
}

// EventsWildcard covers one events version: `{instance}.docs-events.v1.>`. The version is part
// of the wildcard on purpose, like core's `events.v1.>`.
func EventsWildcard(instance string) string {
	return instance + ".docs-events." + EventsVersion + ".>"
}

// NotifySubject is the per-person notification subject.
func NotifySubject(instance, userID, eventType string) string {
	return instance + "." + userID + ".docs-notify." + EventsVersion + "." + eventType
}

// isParam reports whether a pattern segment is a `{param}`.
func isParam(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

// EndpointName is the micro endpoint name of a pattern: `documents.{id}.update` -> `documents-update`.
func EndpointName(pattern string) string {
	var out []string
	for _, s := range strings.Split(pattern, ".") {
		if !isParam(s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, "-")
}

// EndpointSubject is the micro endpoint subject of a pattern: `documents.{id}.update` -> `documents.*.update`.
func EndpointSubject(pattern string) string {
	parts := strings.Split(pattern, ".")
	for i, s := range parts {
		if isParam(s) {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, ".")
}

// MatchPattern matches a method against a pattern and extracts its params.
func MatchPattern(pattern, method string) (map[string]string, bool) {
	ps := strings.Split(pattern, ".")
	ms := strings.Split(method, ".")
	if len(ps) != len(ms) {
		return nil, false
	}
	var params map[string]string
	for i, p := range ps {
		if isParam(p) {
			if ms[i] == "" {
				return nil, false
			}
			if params == nil {
				params = map[string]string{}
			}
			params[p[1:len(p)-1]] = ms[i]
			continue
		}
		if p != ms[i] {
			return nil, false
		}
	}
	return params, true
}

// ParsedSubject is a request subject split in its segments.
type ParsedSubject struct {
	Instance string
	UserID   string
	Service  string
	Version  string
	Method   string
}

// ParseSubject splits `{instance}.{userId}.{service}.{version}.{method...}`.
func ParseSubject(subject string) (ParsedSubject, bool) {
	parts := strings.SplitN(subject, ".", 5)
	if len(parts) != 5 || parts[1] == "" || parts[4] == "" {
		return ParsedSubject{}, false
	}
	return ParsedSubject{
		Instance: parts[0], UserID: parts[1], Service: parts[2], Version: parts[3], Method: parts[4],
	}, true
}

var inboxEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// HashUserID is the inbox hash of a user id: the first 16 characters of
// lowercase(base32_without_padding(sha256(userId))). Same as core and the auth-callout.
func HashUserID(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return strings.ToLower(inboxEncoding.EncodeToString(sum[:])[:16])
}

// InboxPrefix is the only inbox the auth-callout authorises for a user id.
func InboxPrefix(userID string) string { return "_INBOX." + HashUserID(userID) }
