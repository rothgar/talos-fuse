package resourceutil

import (
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v4"

	"github.com/cosi-project/runtime/api/v1alpha1"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/protobuf"
)

// ParseResource parses a YAML or JSON document describing a COSI resource
// and returns the generic *protobuf.Resource representation.
//
// JSON is a subset of YAML, so the same yaml.Unmarshal path handles both
// formats. The metadata block is parsed via resource.Metadata.UnmarshalYAML
// to honour all of the supported metadata fields (namespace/type/id,
// version, owner, phase, labels, annotations, finalizers). The spec block is
// retained as raw YAML text and exposed through Spec.YamlSpec; the server
// side parses YamlSpec into the concrete spec type without requiring the
// client to know that type statically.
func ParseResource(data []byte, format string) (*protobuf.Resource, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty resource document")
	}

	switch format {
	case "yaml", "json":
		// valid
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}

	// When the input is JSON, decode it into a generic map first and
	// re-marshal as YAML so the rest of the pipeline only ever sees
	// YAML bytes. This makes the JSON writeback path explicit and
	// avoids relying on yaml.Unmarshal's permissive JSON parsing.
	if format == "json" {
		var asMap map[string]any
		if err := json.Unmarshal(data, &asMap); err != nil {
			return nil, fmt.Errorf("parse json document: %w", err)
		}
		yamlBytes, err := yaml.Marshal(asMap)
		if err != nil {
			return nil, fmt.Errorf("convert json to yaml: %w", err)
		}
		data = yamlBytes
		format = "yaml"
	}

	var root yaml.Node

	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s document: %w", format, err)
	}

	docNode, err := unwrapDocument(&root)
	if err != nil {
		return nil, fmt.Errorf("parse %s document: %w", format, err)
	}

	if docNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping node at document root, got kind %d", docNode.Kind)
	}

	mdNode, specNode, err := splitMetadataSpec(docNode)
	if err != nil {
		return nil, err
	}

	if mdNode == nil {
		return nil, fmt.Errorf("document is missing required metadata block")
	}

	var md resource.Metadata

	if err := md.UnmarshalYAML(mdNode); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	var specYAML []byte

	if specNode != nil {
		specYAML, err = yaml.Marshal(specNode)
		if err != nil {
			return nil, fmt.Errorf("marshal spec node: %w", err)
		}
	}

	protoRes := &v1alpha1.Resource{
		Metadata: &v1alpha1.Metadata{
			Namespace:   md.Namespace(),
			Type:        md.Type(),
			Id:          md.ID(),
			Version:     md.Version().String(),
			Owner:       md.Owner(),
			Phase:       md.Phase().String(),
			Finalizers:  *md.Finalizers(),
			Annotations: md.Annotations().Raw(),
			Labels:      md.Labels().Raw(),
		},
		Spec: &v1alpha1.Spec{
			YamlSpec: string(specYAML),
		},
	}

	r, err := protobuf.Unmarshal(protoRes)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	return r, nil
}

// unwrapDocument strips a leading yaml.DocumentNode wrapper if present,
// returning the first document's root node.
func unwrapDocument(node *yaml.Node) (*yaml.Node, error) {
	if node.Kind != yaml.DocumentNode {
		return node, nil
	}

	if len(node.Content) == 0 {
		return nil, fmt.Errorf("document node has no content")
	}

	if len(node.Content) != 1 {
		return nil, fmt.Errorf("expected single document, got %d", len(node.Content))
	}

	return node.Content[0], nil
}

// splitMetadataSpec walks a top-level mapping node and returns the value
// nodes for the "metadata" and "spec" keys, in that order. Missing keys
// yield nil entries; nil entries are distinguished from errors so the caller
// can report them appropriately.
func splitMetadataSpec(root *yaml.Node) (*yaml.Node, *yaml.Node, error) {
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("expected mapping node, got kind %d", root.Kind)
	}

	if len(root.Content)%2 != 0 {
		return nil, nil, fmt.Errorf("mapping node has odd number of children: %d", len(root.Content))
	}

	var (
		mdNode   *yaml.Node
		specNode *yaml.Node
	)

	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		value := root.Content[i+1]

		if key.Kind != yaml.ScalarNode {
			return nil, nil, fmt.Errorf("expected scalar key, got kind %d", key.Kind)
		}

		switch key.Value {
		case "metadata":
			mdNode = value
		case "spec":
			specNode = value
		}
	}

	return mdNode, specNode, nil
}
