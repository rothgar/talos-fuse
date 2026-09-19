package fs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/cosi-project/runtime/pkg/resource"

	"github.com/jgarr/talos-fuse/internal/fs"
)

// ----- capturing fake repo -----

// captureRepo records every UpdateResource invocation so the test
// can assert what was written back through the FUSE writeback path.
type captureRepo struct {
	*fakeRepo

	mu       sync.Mutex
	updates  []capturedUpdate
	updateOK bool
}

type capturedUpdate struct {
	Node string
	Meta *resource.Metadata
}

func newCaptureRepo(t *testing.T) *captureRepo {
	t.Helper()

	base := newFakeRepo(t)
	return &captureRepo{
		fakeRepo: base,
		updateOK: true,
	}
}

func (c *captureRepo) UpdateResource(_ context.Context, node string, rsrc resource.Resource) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.updates = append(c.updates, capturedUpdate{
		Node: node,
		Meta: rsrc.Metadata(),
	})

	if !c.updateOK {
		return errors.New("simulated update failure")
	}

	return c.fakeRepo.UpdateResource(context.Background(), node, rsrc)
}

func (c *captureRepo) snapshot() []capturedUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedUpdate, len(c.updates))
	copy(out, c.updates)
	return out
}

// ----- helpers -----

// writeFileAndFlush writes data and closes the file so FUSE emits the
// Flush callback (which is what triggers writeback in this filesystem).
func writeFileAndFlush(t *testing.T, path string, data []byte) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ----- tests -----

func TestWriteback_WriteYAMLRoundTrip(t *testing.T) {
	skipIfNoFuse(t)

	repo := newCaptureRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "yaml",
		Writeable:  true,
		Nodes:      []string{"node-a"},
	})

	target := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.yaml")

	body := `metadata:
  namespace: default
  type: Links.network.talos.dev
  id: eth0
  version: undefined
spec:
  linkStatus:
    speedMbps: 10000
`

	if err := writeFileAndFlush(t, target, []byte(body)); err != nil {
		t.Fatalf("write + flush: %v", err)
	}

	updates := repo.snapshot()
	if len(updates) != 1 {
		t.Fatalf("UpdateResource called %d times, want 1", len(updates))
	}

	got := updates[0]
	if got.Meta.Namespace() != "default" {
		t.Errorf("captured namespace = %q, want %q", got.Meta.Namespace(), "default")
	}
	if got.Meta.Type() != "Links.network.talos.dev" {
		t.Errorf("captured type = %q, want %q", got.Meta.Type(), "Links.network.talos.dev")
	}
	if got.Meta.ID() != "eth0" {
		t.Errorf("captured id = %q, want %q", got.Meta.ID(), "eth0")
	}

	// The same file should now read back the updated spec body.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile after write: %v", err)
	}

	if !strings.Contains(string(data), "speedMbps: 10000") {
		t.Errorf("post-write file missing updated spec:\n%s", data)
	}
}

func TestWriteback_WriteJSONRoundTrip(t *testing.T) {
	skipIfNoFuse(t)

	repo := newCaptureRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "json",
		Writeable:  true,
		Nodes:      []string{"node-a"},
	})

	target := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.json")

	body := `{
  "metadata": {
    "namespace": "default",
    "type": "Links.network.talos.dev",
    "id": "eth0",
    "version": "undefined"
  },
  "spec": {
    "linkStatus": {
      "speedMbps": 2500
    }
  }
}`

	if err := writeFileAndFlush(t, target, []byte(body)); err != nil {
		t.Fatalf("write + flush: %v", err)
	}

	updates := repo.snapshot()
	if len(updates) != 1 {
		t.Fatalf("UpdateResource called %d times, want 1", len(updates))
	}

	if got := updates[0].Meta.ID(); got != "eth0" {
		t.Errorf("captured id = %q, want eth0", got)
	}

	// Read back and check the spec body.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile after write: %v", err)
	}
	if !strings.Contains(string(data), "\"speedMbps\": 2500") {
		t.Errorf("post-write file missing updated spec:\n%s", data)
	}
}

func TestWriteback_IDMismatchRejected(t *testing.T) {
	skipIfNoFuse(t)

	repo := newCaptureRepo(t)

	dir := mountTemp(t, &fs.Options{
		Repository: repo,
		Format:     "yaml",
		Writeable:  true,
		Nodes:      []string{"node-a"},
	})

	target := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.yaml")

	// Filename says "eth0.yaml" but the document claims id "eth1".
	body := `metadata:
  namespace: default
  type: Links.network.talos.dev
  id: eth1
spec:
  linkStatus:
    speedMbps: 100
`

	if err := writeFileAndFlush(t, target, []byte(body)); err == nil {
		t.Fatalf("expected error for id mismatch, got nil")
	}

	updates := repo.snapshot()
	if len(updates) != 0 {
		t.Errorf("UpdateResource called %d times after rejected write, want 0", len(updates))
	}
}

func TestWriteback_MaintenanceRejectsWrite(t *testing.T) {
	skipIfNoFuse(t)

	repo := newCaptureRepo(t)

	// Maintenance implies Writeable==false regardless of caller intent.
	dir := mountTemp(t, &fs.Options{
		Repository:  repo,
		Format:      "yaml",
		Writeable:   true, // would normally allow writes
		Maintenance: true,
		Nodes:       []string{"node-a"},
	})

	target := filepath.Join(dir, "nodes", "node-a", "default", "linksstatus", "eth0.yaml")

	err := writeFileAndFlush(t, target, []byte("anything"))
	if err == nil {
		t.Fatalf("expected write to fail in maintenance mode")
	}

	if !isReadOnlyErr(err) && !errors.Is(err, syscall.EROFS) {
		t.Errorf("expected EROFS-like error in maintenance mode, got %v", err)
	}

	if len(repo.snapshot()) != 0 {
		t.Errorf("UpdateResource should not be invoked in maintenance mode")
	}
}
