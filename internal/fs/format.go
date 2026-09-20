// Package fs provides the FUSE tree exposing Talos resources as files.
package fs

import (
	"fmt"
	"strings"
)

// escapeID replaces '/' with '+' so that resource IDs containing
// slashes can appear as a single filename component on disk.
func escapeID(id string) string {
	return strings.ReplaceAll(id, "/", "+")
}

// unescapeID is the inverse of escapeID.
func unescapeID(name string) string {
	return strings.ReplaceAll(name, "+", "/")
}

// fileExtensionFor returns the file extension associated with the given
// format string. It accepts "yaml" and "json"; the default is "yaml".
func fileExtensionFor(format string) string {
	if strings.EqualFold(format, "json") {
		return ".json"
	}
	return ".yaml"
}

// splitIDAndExtension splits a base filename into the escaped id and
// extension. The expected extension is one of the formats supported by
// format string ("yaml" or "json"). An unknown extension returns an error.
func splitIDAndExtension(name string) (id, ext string, err error) {
	for _, e := range []string{".yaml", ".yml", ".json"} {
		if strings.HasSuffix(name, e) {
			return strings.TrimSuffix(name, e), e, nil
		}
	}
	return "", "", fmt.Errorf("unsupported file extension on %q", name)
}

// normalizeFormat lower-cases the format string and defaults to yaml
// for unknown values.
func normalizeFormat(format string) string {
	switch strings.ToLower(format) {
	case "json":
		return "json"
	default:
		return "yaml"
	}
}
