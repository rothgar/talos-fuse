// Package resourceutil provides helpers for converting COSI resources
// between YAML/JSON document form and the generic protobuf resource
// representation used by talos-fuse.
package resourceutil

import (
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v4"

	"github.com/cosi-project/runtime/pkg/resource"
)

// FormatResource renders the given resource as either YAML or JSON bytes.
//
// The intermediate representation is produced by resource.MarshalYAML, which
// returns an any value describing the resource in terms of basic Go types
// and map[string]any/slice structures. The result is then re-encoded as
// either YAML or JSON. JSON output is indented with two spaces.
//
// An unsupported format returns an error; callers should normalize the
// format string before calling this function.
func FormatResource(r resource.Resource, format string) ([]byte, error) {
	v, err := resource.MarshalYAML(r)
	if err != nil {
		return nil, fmt.Errorf("marshal resource to intermediate form: %w", err)
	}

	switch format {
	case "yaml":
		out, err := yaml.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal resource to yaml: %w", err)
		}

		return out, nil
	case "json":
		// Use YAML as the intermediate so that custom MarshalYAML
		// implementations (e.g. resource.Metadata) are honored, then
		// convert to a map and emit JSON with lowercase keys.
		yamlBytes, err := yaml.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal resource to yaml: %w", err)
		}

		var data map[string]any
		if err := yaml.Unmarshal(yamlBytes, &data); err != nil {
			return nil, fmt.Errorf("convert yaml to json intermediate: %w", err)
		}

		out, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal resource to json: %w", err)
		}

		return out, nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}
