//go:build e2e

// Package e2e_test exercises talos-fuse against a real Talos cluster.
//
// Prerequisites:
//   - /dev/fuse must be accessible on the host (load the fuse kernel module).
//   - Either talosctl must be in PATH (to create a local Docker cluster), or
//     an existing cluster must be pointed at via env vars (see below).
//
// Environment variables:
//
//	E2E_TALOSCONFIG  Path to an existing talosconfig. When set the test does
//	                 not create or destroy a cluster and uses this file for
//	                 all Talos API calls.
//	E2E_CONTEXT     Talosconfig context name. Defaults to the file's active
//	                context when unset.
//	E2E_NODES       Comma-separated node IPs/hostnames. Inferred from the
//	                talosconfig context when unset.
//
// When none of the above are set the test creates a single-controlplane
// Docker cluster named "talos-fuse-e2e" and tears it down after the run.
// Run with:
//
//	go test -tags e2e -v -timeout 15m ./e2e/...
package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"

	"github.com/jgarr/talos-fuse/internal/fs"
	"github.com/jgarr/talos-fuse/internal/talos"
)

const managedClusterName = "talos-fuse-e2e"

// Globals populated by TestMain before any test runs.
var (
	testTalosconfig string
	testContextName string
	testNodes       []string
)

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: SKIP – /dev/fuse not available")
		return 0
	}

	// Use a pre-existing cluster when pointed at one via env vars.
	if extCfg := os.Getenv("E2E_TALOSCONFIG"); extCfg != "" {
		testTalosconfig = extCfg
		testContextName = os.Getenv("E2E_CONTEXT")
		if raw := os.Getenv("E2E_NODES"); raw != "" {
			testNodes = strings.Split(raw, ",")
		}
		if len(testNodes) == 0 {
			if err := discoverNodes(); err != nil {
				fmt.Fprintf(os.Stderr, "e2e: discover nodes: %v\n", err)
				return 1
			}
		}
		fmt.Printf("e2e: using existing cluster; context=%q nodes=%v\n", testContextName, testNodes)
		return m.Run()
	}

	// No external cluster: create a local Docker cluster.
	if _, err := exec.LookPath("talosctl"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: SKIP – talosctl not in PATH and E2E_TALOSCONFIG not set")
		return 0
	}

	tmpDir, err := os.MkdirTemp("", "talos-fuse-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: mktemp: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	stateDir := filepath.Join(tmpDir, "state")
	testTalosconfig = filepath.Join(tmpDir, "config")
	testContextName = managedClusterName

	fmt.Printf("e2e: creating cluster %q\n", managedClusterName)
	if err := createCluster(stateDir, testTalosconfig); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: cluster create: %v\n", err)
		return 1
	}
	defer destroyCluster(stateDir)

	if err := discoverNodes(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: discover nodes: %v\n", err)
		return 1
	}

	fmt.Printf("e2e: cluster ready; nodes=%v\n", testNodes)
	return m.Run()
}

// createCluster runs `talosctl cluster create docker`, writing cluster state to
// stateDir and the generated talosconfig to talosconfig.
func createCluster(stateDir, talosconfig string) error {
	cmd := exec.Command(
		"talosctl", "cluster", "create", "docker",
		"--name", managedClusterName,
		"--state", stateDir,
		"--workers", "0",
		"--talosconfig-destination", talosconfig,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// destroyCluster tears down the managed cluster unconditionally.
func destroyCluster(stateDir string) {
	cmd := exec.Command(
		"talosctl", "cluster", "destroy",
		"--name", managedClusterName,
		"--state", stateDir,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: cluster destroy: %v\n", err)
	}
}

// discoverNodes reads testTalosconfig and populates testNodes from the
// effective context. Nodes takes priority; Endpoints is used as a
// fallback so that Docker-provisioned talosconfigss (which set Endpoints
// but not Nodes) work without requiring E2E_NODES.
func discoverNodes() error {
	cfg, err := clientconfig.Open(testTalosconfig)
	if err != nil {
		return fmt.Errorf("open %q: %w", testTalosconfig, err)
	}

	name := testContextName
	if name == "" {
		name = cfg.Context
		testContextName = name
	}

	ctx := cfg.Contexts[name]
	if ctx == nil {
		return fmt.Errorf("context %q not found in %q", name, testTalosconfig)
	}

	nodes := ctx.Nodes
	if len(nodes) == 0 {
		nodes = ctx.Endpoints
	}
	if len(nodes) == 0 {
		return fmt.Errorf("context %q in %q has no nodes or endpoints; pass E2E_NODES", name, testTalosconfig)
	}

	testNodes = append(testNodes[:0], nodes...)
	return nil
}

// ----- mount helpers -----

// mountWith mounts a talos-fuse filesystem with the given Options and returns
// the mountpoint. The server is unmounted when t ends.
func mountWith(t *testing.T, opts *fs.Options) string {
	t.Helper()

	dir := t.TempDir()
	server, err := fs.Mount(dir, &fs.TalosRoot{}, opts)
	if err != nil {
		t.Fatalf("fs.Mount: %v", err)
	}
	if err := server.WaitMount(); err != nil {
		t.Fatalf("server.WaitMount: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })
	return dir
}

// newAdapter constructs a RepositoryAdapter for the e2e cluster.
// Cluster is intentionally left empty: the talosconfig context already
// carries the correct Omni cluster name; passing it here would call
// client.WithCluster with the context name ("jg-wrtk8s") rather than
// the actual cluster name ("wrtk8s") and break Omni routing.
func newAdapter() *fs.RepositoryAdapter {
	return &fs.RepositoryAdapter{
		Repo:        talos.NewRepository(false, nil, nil),
		Talosconfig: testTalosconfig,
		ContextName: testContextName,
		Cluster:     "",
	}
}

// defaultOpts returns a base Options struct for the e2e cluster.
func defaultOpts(cluster, format string) *fs.Options {
	return &fs.Options{
		Repository:  newAdapter(),
		Format:      format,
		Nodes:       append([]string(nil), testNodes...),
		Cluster:     cluster,
	}
}

// ----- tests -----

// TestE2E_ClusterNameAtRoot verifies that when Options.Cluster is set, the
// top-level directory in the mounted tree carries the cluster name.
func TestE2E_ClusterNameAtRoot(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "yaml"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(root): %v", err)
	}

	for _, e := range entries {
		if e.Name() == testContextName && e.IsDir() {
			return
		}
	}
	t.Fatalf("root dir missing %q; got %v", testContextName, dirNames(entries))
}

// TestE2E_NodesDefaultFallback verifies that the top-level directory falls
// back to "nodes" when Options.Cluster is empty.
func TestE2E_NodesDefaultFallback(t *testing.T) {
	dir := mountWith(t, defaultOpts("", "yaml"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(root): %v", err)
	}

	for _, e := range entries {
		if e.Name() == "nodes" && e.IsDir() {
			return
		}
	}
	t.Fatalf("root dir missing \"nodes\" fallback; got %v", dirNames(entries))
}

// TestE2E_NodeDirsPresent verifies that each configured node appears as a
// subdirectory under the cluster top-level directory.
func TestE2E_NodeDirsPresent(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "yaml"))
	clusterDir := filepath.Join(dir, testContextName)

	entries, err := os.ReadDir(clusterDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", clusterDir, err)
	}

	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}

	for _, node := range testNodes {
		if !got[node] {
			t.Errorf("cluster dir missing node %q; got %v", node, dirNames(entries))
		}
	}
}

// TestE2E_NamespaceDirsPresent verifies that at least one namespace
// directory exists under every configured node.
func TestE2E_NamespaceDirsPresent(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "yaml"))

	for _, node := range testNodes {
		nodePath := filepath.Join(dir, testContextName, node)
		entries, err := os.ReadDir(nodePath)
		if err != nil {
			t.Fatalf("node %q: ReadDir(%q): %v", node, nodePath, err)
		}
		if len(entries) == 0 {
			t.Errorf("node %q: no namespace dirs found under %q", node, nodePath)
		}
	}
}

// TestE2E_ResourceTypesDirNonEmpty verifies that the first available
// namespace exposes at least one resource-type directory for each node.
func TestE2E_ResourceTypesDirNonEmpty(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "yaml"))

	for _, node := range testNodes {
		nodePath := filepath.Join(dir, testContextName, node)
		nsPath := firstDir(t, nodePath, "namespace")
		entries, err := os.ReadDir(nsPath)
		if err != nil {
			t.Fatalf("node %q: ReadDir(%q): %v", node, nsPath, err)
		}
		if len(entries) == 0 {
			t.Errorf("node %q: namespace dir %q is empty", node, nsPath)
		}
	}
}

// TestE2E_ResourceFileYAML reads a resource file from the first available
// namespace on each node and validates it contains YAML with a metadata block.
func TestE2E_ResourceFileYAML(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "yaml"))

	for _, node := range testNodes {
		nodePath := filepath.Join(dir, testContextName, node)
		nsPath := firstDir(t, nodePath, "namespace")
		typePath := firstDir(t, nsPath, "resource type")
		filePath := firstFile(t, typePath, "resource file")

		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("node %q: ReadFile(%q): %v", node, filePath, err)
		}
		if len(data) == 0 {
			t.Errorf("node %q: %q is empty", node, filePath)
		}
		if !strings.Contains(string(data), "metadata:") {
			t.Errorf("node %q: %q missing 'metadata:' block:\n%s", node, filePath, data)
		}
	}
}

// TestE2E_ResourceFileJSON mounts with JSON format and validates that
// resource files are parseable JSON objects containing a "metadata" key.
func TestE2E_ResourceFileJSON(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "json"))

	for _, node := range testNodes {
		nodePath := filepath.Join(dir, testContextName, node)
		nsPath := firstDir(t, nodePath, "namespace")
		typePath := firstDir(t, nsPath, "resource type")
		filePath := firstFile(t, typePath, "resource file")

		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("node %q: ReadFile(%q): %v", node, filePath, err)
		}

		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("node %q: %q is not valid JSON: %v\n%s", node, filePath, err, data)
		}
		if _, ok := parsed["metadata"]; !ok {
			t.Errorf("node %q: JSON missing 'metadata' key:\n%s", node, data)
		}
	}
}

// TestE2E_UnknownNodeReturnsENOENT verifies that looking up a non-existent
// node path under the cluster dir returns a not-found error.
func TestE2E_UnknownNodeReturnsENOENT(t *testing.T) {
	dir := mountWith(t, defaultOpts(testContextName, "yaml"))
	bogus := filepath.Join(dir, testContextName, "node-that-does-not-exist")
	if _, err := os.Stat(bogus); err == nil {
		t.Fatalf("Stat(%q) succeeded; expected ENOENT", bogus)
	}
}

// ----- helpers -----

// firstDir returns the path to the first directory entry inside parent,
// failing the test if there are none.
func firstDir(t *testing.T, parent, label string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", parent, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(parent, e.Name())
		}
	}
	t.Fatalf("no %s found in %q", label, parent)
	return ""
}

// firstFile returns the path to the first regular file entry inside parent,
// failing the test if there are none.
func firstFile(t *testing.T, parent, label string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", parent, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return filepath.Join(parent, e.Name())
		}
	}
	t.Fatalf("no %s found in %q", label, parent)
	return ""
}

// dirNames extracts entry names for use in test failure messages.
func dirNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}
