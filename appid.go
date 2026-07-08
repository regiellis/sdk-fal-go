package fal

import (
	"fmt"
	"regexp"
	"strings"
)

// appNamespaces are the recognized leading path segments that group apps under
// a namespace rather than an owner.
var appNamespaces = map[string]struct{}{
	"workflows": {},
	"comfy":     {},
}

// legacyAppIDPattern matches the legacy numeric-prefix form "12345-alias",
// which is normalized to "12345/alias".
var legacyAppIDPattern = regexp.MustCompile(`^([0-9]+)-([a-zA-Z0-9-]+)$`)

// appID is a parsed fal application identifier.
type appID struct {
	// Owner is the account that owns the app.
	Owner string
	// Alias is the app name.
	Alias string
	// Path is the optional trailing path after owner and alias, without a
	// leading slash; empty when absent.
	Path string
	// Namespace is the optional leading namespace ("workflows" or "comfy");
	// empty when absent.
	Namespace string
}

// parseAppID parses an application identifier in one of these forms:
//
//	owner/alias[/path...]
//	namespace/owner/alias[/path...]   (namespace is "workflows" or "comfy")
//	12345-alias                       (legacy, normalized to 12345/alias)
//
// It returns an error for input that does not match any of these shapes.
func parseAppID(id string) (appID, error) {
	normalized, err := normalizeAppID(id)
	if err != nil {
		return appID{}, err
	}

	parts := strings.Split(normalized, "/")
	if _, ok := appNamespaces[parts[0]]; ok {
		if len(parts) < 3 {
			return appID{}, fmt.Errorf("fal: invalid app id %q: namespace form requires namespace/owner/alias", id)
		}
		result := appID{
			Namespace: parts[0],
			Owner:     parts[1],
			Alias:     parts[2],
			Path:      strings.Join(parts[3:], "/"),
		}
		if result.Owner == "" || result.Alias == "" {
			return appID{}, fmt.Errorf("fal: invalid app id %q: owner and alias must not be empty", id)
		}
		return result, nil
	}

	result := appID{
		Owner: parts[0],
		Alias: parts[1],
		Path:  strings.Join(parts[2:], "/"),
	}
	if result.Owner == "" || result.Alias == "" {
		return appID{}, fmt.Errorf("fal: invalid app id %q: owner and alias must not be empty", id)
	}
	return result, nil
}

// normalizeAppID rewrites the legacy numeric-prefix form into owner/alias and
// otherwise returns identifiers that already contain a slash unchanged. Input
// with no slash and no legacy match is rejected.
func normalizeAppID(id string) (string, error) {
	if strings.Contains(id, "/") {
		return id, nil
	}
	if m := legacyAppIDPattern.FindStringSubmatch(id); m != nil {
		return m[1] + "/" + m[2], nil
	}
	return "", fmt.Errorf("fal: invalid app id %q: must be in the format owner/alias", id)
}

// path returns the app's path segment beneath a base host, in the form
// "[namespace/]owner/alias[/path]".
func (a appID) path() string {
	var b strings.Builder
	if a.Namespace != "" {
		b.WriteString(a.Namespace)
		b.WriteByte('/')
	}
	b.WriteString(a.Owner)
	b.WriteByte('/')
	b.WriteString(a.Alias)
	if a.Path != "" {
		b.WriteByte('/')
		b.WriteString(a.Path)
	}
	return b.String()
}
