package fs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/cosi-project/runtime/api/v1alpha1"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/resource/protobuf"

	"github.com/jgarr/talos-fuse/internal/fs"
)

// ----- fake ResourceRepository -----

// fakeRepo is an in-memory ResourceRepository implementation used by
// the FUSE tests. It maps (node, namespace, type, id) tuples to either
// a deterministic resource or an error.
type fakeRepo struct {
	mu sync.Mutex

	defs []*meta.ResourceDefinition
	// items[key] -> resource
	items map[string]resource.Resource
	// errs[key] -> error returned in preference to items[key]
	errs map[string]error
}

func newFakeRepo(t *testing.T) *fakeRepo {
	t.Helper()

	rdLinks, err := meta.NewResourceDefinition(meta.ResourceDefinitionSpec{
		Type:             "Links.network.talos.dev",
		DisplayType:      "LinkStatus",
		DefaultNamespace: "default",
		Aliases:          []resource.Type{"linksstatus"},
	})
	if err != nil {
		t.Fatalf("build Links resource definition: %v", err)
	}

	rdMembership, err := meta.NewResourceDefinition(meta.ResourceDefinitionSpec{
		Type:             "MemberStatuses.cluster.talos.dev",
		DisplayType:      "MemberStatus",
		DefaultNamespace: "cluster",
		Aliases:          []resource.Type{"memberstatuses"},
	})
	if err != nil {
		t.Fatalf("build MemberStatuses resource definition: %v", err)
	}

	linkResource := buildProtobufResource(t, "default", "Links.network.talos.dev", "eth0")
	memberResource := buildProtobufResource(t, "cluster", "MemberStatuses.cluster.talos.dev", "node-1")

	return &fakeRepo{
		defs: []*meta.ResourceDefinition{rdLinks, rdMembership},
		items: map[string]resource.Resource{
			"node-a/default/Links.network.talos.dev/eth0":            linkResource,
			"node-a/cluster/MemberStatuses.cluster.talos.dev/node-1": memberResource,
		},
		errs: map[string]error{},
	}
}

func (f *fakeRepo) ResourceDefinitions(_ context.Context) ([]*meta.ResourceDefinition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*meta.ResourceDefinition, len(f.defs))
	copy(out, f.defs)
	return out, nil
}

func (f *fakeRepo) ListResources(_ context.Context, node, namespace, resourceType string) ([]resource.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []resource.Resource
	for k, v := range f.items {
		if !keyMatches(k, node, namespace, resourceType, "") {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeRepo) GetResource(_ context.Context, node, namespace, resourceType, id string) (resource.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := repoKey(node, namespace, resourceType, id)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	v, ok := f.items[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

func (f *fakeRepo) UpdateResource(_ context.Context, node string, rsrc resource.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := repoKey(node, string(rsrc.Metadata().Namespace()), string(rsrc.Metadata().Type()), string(rsrc.Metadata().ID()))
	f.items[key] = rsrc
	return nil
}

func (f *fakeRepo) setErr(node, namespace, resourceType, id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[repoKey(node, namespace, resourceType, id)] = err
}

func repoKey(node, namespace, resourceType, id string) string {
	if node == "" {
		node = "_"
	}
	return node + "/" + namespace + "/" + resourceType + "/" + id
}

func keyMatches(key, node, namespace, resourceType, id string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return false
	}
	if node != "" && parts[0] != node {
		return false
	}
	if parts[1] != namespace || parts[2] != resourceType {
		return false
	}
	if id != "" && parts[3] != id {
		return false
	}
	return true
}

// ----- test resource construction -----

func buildProtobufResource(t *testing.T, namespace, typ, id string) *protobuf.Resource {
	t.Helper()

	md := resource.NewMetadata(resource.Namespace(namespace), resource.Type(typ), resource.ID(id), resource.VersionUndefined)

	proto := &v1alpha1.Resource{
		Metadata: &v1alpha1.Metadata{
			Namespace: md.Namespace(),
			Type:      md.Type(),
			Id:        md.ID(),
			Version:   md.Version().String(),
			Phase:     resource.PhaseRunning.String(),
		},
		Spec: &v1alpha1.Spec{
			YamlSpec: "value: hello-from-test\n",
		},
	}

	r, err := protobuf.Unmarshal(proto)
	if err != nil {
		t.Fatalf("unmarshal protobuf resource: %v", err)
	}
	return r
}

// ----- helpers -----

// skipIfNoFuse skips the calling test when the host does not provide
// the FUSE device (no fusermount or /dev/fuse).
func skipIfNoFuse(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse not available; skipping FUSE test")
	}
}

// mountTemp mounts a fresh talos-fuse filesystem in a temp directory
// and registers cleanup that unmounts it when the test finishes.
func mountTemp(t *testing.T, opts *fs.Options) string {
	t.Helper()

	dir := t.TempDir()

	server, err := fs.Mount(dir, &fs.TalosRoot{}, opts)
	if err != nil {
		t.Fatalf("Mount(%q): %v", dir, err)
	}

	if err := server.WaitMount(); err != nil {
		t.Fatalf("WaitMount: %v", err)
	}

	t.Cleanup(func() { _ = server.Unmount() })
	return dir
}

// ----- tests -----

func TestMount_ReadOnly_ReaddirLookupAndYAML(t *testing.T) {
	skipIfNoFuse(t)

	repo := newFakeRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "yaml",
		Nodes:      []string{"node-a"},
	})

	// Readdir on root always returns a "nodes" directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(root): %v", err)
	}

	gotNames := map[string]bool{}
	for _, e := range entries {
		gotNames[e.Name()] = true
	}

	if !gotNames["nodes"] {
		t.Fatalf("root dir missing 'nodes' directory (got %v)", gotNames)
	}

	// Walk into /nodes/node-a/.
	nodeDir := filepath.Join(dir, "nodes", "node-a")
	nodeEntries, err := os.ReadDir(nodeDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", nodeDir, err)
	}

	gotNS := map[string]bool{}
	for _, e := range nodeEntries {
		gotNS[e.Name()] = true
	}

	for _, want := range []string{"default", "cluster"} {
		if !gotNS[want] {
			t.Errorf("per-node dir missing namespace %q (got %v)", want, gotNS)
		}
	}

	// Walk into /nodes/node-a/default/linksstatus/.
	nsDir := filepath.Join(nodeDir, "default")
	nsEntries, err := os.ReadDir(nsDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", nsDir, err)
	}

	if len(nsEntries) == 0 {
		t.Fatalf("namespace dir %q is empty", nsDir)
	}

	found := false
	for _, e := range nsEntries {
		if e.Name() == "linksstatus" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("namespace dir %q missing linksstatus entry (got %v)", nsDir, names(nsEntries))
	}

	typeDir := filepath.Join(nsDir, "linksstatus")
	typeEntries, err := os.ReadDir(typeDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", typeDir, err)
	}

	if len(typeEntries) != 1 || typeEntries[0].Name() != "eth0.yaml" {
		t.Fatalf("type dir %q: got %v, want [eth0.yaml]", typeDir, names(typeEntries))
	}

	// Read the file and check for the expected YAML metadata keys.
	data, err := os.ReadFile(filepath.Join(typeDir, "eth0.yaml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	yaml := string(data)
	for _, want := range []string{"namespace: default", "type: Links.network.talos.dev", "id: eth0"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("yaml file missing %q:\n%s", want, yaml)
		}
	}
}

func TestMount_MultiNodeLayout(t *testing.T) {
	skipIfNoFuse(t)

	repo := newFakeRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "yaml",
		Nodes:      []string{"node-a", "node-b"},
	})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(root): %v", err)
	}

	var foundNodes bool
	for _, e := range entries {
		if e.Name() == "nodes" {
			foundNodes = true
		}
	}
	if !foundNodes {
		t.Fatalf("multi-node root missing 'nodes' directory (got %v)", names(entries))
	}

	// /nodes/<node>/<namespace>/<resource-type>/<id>.<ext> must be
	// reachable for every configured node.
	for _, node := range []string{"node-a", "node-b"} {
		target := filepath.Join(dir, "nodes", node)
		if _, err := os.Stat(target); err != nil {
			t.Errorf("missing per-node dir %q: %v", target, err)
		}
	}

	// /nodes/node-a/cluster/memberstatuses/node-1.yaml must exist
	// for node-a (the fake repo only populates node-a).
	clusterTarget := filepath.Join(dir, "nodes", "node-a", "cluster", "memberstatuses", "node-1.yaml")
	if _, err := os.Stat(clusterTarget); err != nil {
		t.Errorf("expected cluster resource at %q: %v", clusterTarget, err)
	}
}

func TestMount_MultiNodeNodeEntryRead(t *testing.T) {
	skipIfNoFuse(t)

	repo := newFakeRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "yaml",
		Nodes:      []string{"node-a", "node-b"},
	})

	// /nodes must list the configured node names.
	nodesDir := filepath.Join(dir, "nodes")
	entries, err := os.ReadDir(nodesDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", nodesDir, err)
	}

	gotNames := map[string]bool{}
	for _, e := range entries {
		gotNames[e.Name()] = true
	}

	for _, want := range []string{"node-a", "node-b"} {
		if !gotNames[want] {
			t.Errorf("/nodes missing %q (got %v)", want, gotNames)
		}
	}

	// Walking /nodes/node-a/default/linksstatus/eth0.yaml must succeed.
	target := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", target, err)
	}

	body := string(data)
	for _, want := range []string{"namespace: default", "type: Links.network.talos.dev", "id: eth0"} {
		if !strings.Contains(body, want) {
			t.Errorf("multi-node yaml file missing %q:\n%s", want, body)
		}
	}
}

func TestMount_JSONFormat(t *testing.T) {
	skipIfNoFuse(t)

	repo := newFakeRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "json",
		Nodes:      []string{"node-a"},
	})

	path := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v\nbody: %s", err, data)
	}

	md, ok := parsed["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("json output missing metadata block: %s", data)
	}

	if got, want := md["namespace"], "default"; got != want {
		t.Errorf("metadata.namespace = %v, want %q", got, want)
	}
	if got, want := md["id"], "eth0"; got != want {
		t.Errorf("metadata.id = %v, want %q", got, want)
	}
}

func TestMount_ReadOnlyWriteFails(t *testing.T) {
	skipIfNoFuse(t)

	repo := newFakeRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "yaml",
		Writeable:  false,
		Nodes:      []string{"node-a"},
	})

	target := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.yaml")

	err := os.WriteFile(target, []byte("not allowed"), 0o644)
	if err == nil {
		t.Fatalf("WriteFile to read-only mount unexpectedly succeeded")
	}

	if !isReadOnlyErr(err) {
		t.Errorf("expected EACCES/EROFS, got %v", err)
	}
}

// isReadOnlyErr reports whether err is or wraps a syscall that
// indicates the filesystem refused the write. We accept both
// generic errors (returned by the FUSE library) and syscall.Errno
// values (returned when writeback goes through the kernel directly).
func isReadOnlyErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EPERM) {
		return true
	}
	// Some paths return plain text errors ("read-only file system").
	msg := err.Error()
	if strings.Contains(msg, "read-only") || strings.Contains(msg, "permission denied") {
		return true
	}
	return false
}

func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}
