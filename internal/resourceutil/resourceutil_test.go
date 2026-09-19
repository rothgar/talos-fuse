package resourceutil_test

import (
	"strings"
	"testing"

	"github.com/cosi-project/runtime/api/v1alpha1"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/protobuf"

	"github.com/jgarr/talos-fuse/internal/resourceutil"
)

// buildResource constructs a generic protobuf.Resource with deterministic
// metadata and a small spec body. Used by both round-trip tests.
func mustVersion(t *testing.T, s string) resource.Version {
	t.Helper()

	v, err := resource.ParseVersion(s)
	if err != nil {
		t.Fatalf("parse version %q: %v", s, err)
	}

	return v
}

func buildResource(t *testing.T) *protobuf.Resource {
	t.Helper()

	md := resource.NewMetadata("default", "LinksStatus", "eth0", mustVersion(t, "2"))
	md.SetOwner("NetworkOperator")
	md.Finalizers().Add("example.finalizer")
	md.Labels().Set("zone", "a")
	md.Annotations().Set("note", "hello")

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
			YamlSpec: "linkStatus:\n  speedMbps: 1000\n  duplex: full\n",
		},
	}

	r, err := protobuf.Unmarshal(protoRes)
	if err != nil {
		t.Fatalf("build protobuf.Resource: %v", err)
	}

	return r
}

func TestFormatResource_YAML(t *testing.T) {
	r := buildResource(t)

	out, err := resourceutil.FormatResource(r, "yaml")
	if err != nil {
		t.Fatalf("FormatResource(yaml): %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "namespace: default") {
		t.Errorf("yaml output missing namespace:\n%s", s)
	}

	if !strings.Contains(s, "type: LinksStatus") {
		t.Errorf("yaml output missing type:\n%s", s)
	}

	if !strings.Contains(s, "id: eth0") {
		t.Errorf("yaml output missing id:\n%s", s)
	}

	if !strings.Contains(s, "linkStatus:") {
		t.Errorf("yaml output missing spec body:\n%s", s)
	}
}

func TestFormatResource_JSON(t *testing.T) {
	r := buildResource(t)

	out, err := resourceutil.FormatResource(r, "json")
	if err != nil {
		t.Fatalf("FormatResource(json): %v", err)
	}

	s := string(out)
	if !strings.HasPrefix(s, "{") {
		t.Errorf("expected json output to start with '{', got: %q", s[:min(len(s), 1)])
	}

	if !strings.Contains(s, "\"namespace\": \"default\"") {
		t.Errorf("json output missing namespace:\n%s", s)
	}

	if !strings.Contains(s, "\"id\": \"eth0\"") {
		t.Errorf("json output missing id:\n%s", s)
	}

	if !strings.Contains(s, "\"linkStatus\"") {
		t.Errorf("json output missing spec body:\n%s", s)
	}
}

func TestFormatResource_UnsupportedFormat(t *testing.T) {
	r := buildResource(t)

	if _, err := resourceutil.FormatResource(r, "toml"); err == nil {
		t.Fatal("FormatResource(toml) returned nil error, want non-nil")
	}
}

func TestParseResource_YAML_RoundTrip(t *testing.T) {
	original := buildResource(t)

	doc, err := resourceutil.FormatResource(original, "yaml")
	if err != nil {
		t.Fatalf("FormatResource(yaml): %v", err)
	}

	parsed, err := resourceutil.ParseResource(doc, "yaml")
	if err != nil {
		t.Fatalf("ParseResource(yaml): %v", err)
	}

	if got, want := parsed.Metadata().Namespace(), original.Metadata().Namespace(); got != want {
		t.Errorf("namespace: got %q, want %q", got, want)
	}

	if got, want := parsed.Metadata().Type(), original.Metadata().Type(); got != want {
		t.Errorf("type: got %q, want %q", got, want)
	}

	if got, want := parsed.Metadata().ID(), original.Metadata().ID(); got != want {
		t.Errorf("id: got %q, want %q", got, want)
	}

	if got, want := parsed.Metadata().Owner(), original.Metadata().Owner(); got != want {
		t.Errorf("owner: got %q, want %q", got, want)
	}

	if got, want := parsed.Metadata().Version().String(), original.Metadata().Version().String(); got != want {
		t.Errorf("version: got %q, want %q", got, want)
	}

	if got, want := *parsed.Metadata().Finalizers(), *original.Metadata().Finalizers(); !slicesEqual(got, want) {
		t.Errorf("finalizers: got %v, want %v", got, want)
	}

	if got, want := parsed.Metadata().Labels().Raw(), original.Metadata().Labels().Raw(); !mapsEqual(got, want) {
		t.Errorf("labels: got %v, want %v", got, want)
	}

	if got, want := parsed.Metadata().Annotations().Raw(), original.Metadata().Annotations().Raw(); !mapsEqual(got, want) {
		t.Errorf("annotations: got %v, want %v", got, want)
	}
}

func TestParseResource_JSON_RoundTrip(t *testing.T) {
	original := buildResource(t)

	doc, err := resourceutil.FormatResource(original, "json")
	if err != nil {
		t.Fatalf("FormatResource(json): %v", err)
	}

	parsed, err := resourceutil.ParseResource(doc, "json")
	if err != nil {
		t.Fatalf("ParseResource(json): %v", err)
	}

	if got, want := parsed.Metadata().Namespace(), original.Metadata().Namespace(); got != want {
		t.Errorf("namespace: got %q, want %q", got, want)
	}

	if got, want := parsed.Metadata().ID(), original.Metadata().ID(); got != want {
		t.Errorf("id: got %q, want %q", got, want)
	}

	if got, want := parsed.Metadata().Type(), original.Metadata().Type(); got != want {
		t.Errorf("type: got %q, want %q", got, want)
	}
}

func TestParseResource_UnsupportedFormat(t *testing.T) {
	if _, err := resourceutil.ParseResource([]byte("metadata:\n  namespace: x\n"), "toml"); err == nil {
		t.Fatal("ParseResource(toml) returned nil error, want non-nil")
	}
}

func TestParseResource_MissingMetadata(t *testing.T) {
	doc := []byte("spec:\n  foo: bar\n")

	if _, err := resourceutil.ParseResource(doc, "yaml"); err == nil {
		t.Fatal("ParseResource without metadata returned nil error, want non-nil")
	}
}

func TestParseResource_EmptyDocument(t *testing.T) {
	if _, err := resourceutil.ParseResource(nil, "yaml"); err == nil {
		t.Fatal("ParseResource(nil) returned nil error, want non-nil")
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}
