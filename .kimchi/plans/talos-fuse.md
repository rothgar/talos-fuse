# talos-fuse Plan

## Goal

Build a read-only-by-default FUSE CLI tool, `talos-fuse`, that exposes Talos API resources as a directory tree. It uses the Talos machinery client (the same library `talosctl` uses) with existing `talosconfig` authentication. When `--write` is enabled, saving a resource file pushes the resource back via the COSI resource API. Maintenance mode is supported via `--maintenance`, which connects with insecure TLS and exposes only the resources the maintenance API permits. Streaming APIs (e.g. `dmesg`, `tcpdump`) and `etcd` subcommands are explicitly out of scope; the tool is focused on the equivalent of `talosctl get <resource>`.

## Constraints

- Go 1.26+ (required by `github.com/siderolabs/talos/pkg/machinery@v1.13.7`, verified on pkg.go.dev).
- Use `github.com/hanwen/go-fuse/v2` for the FUSE layer.
- Use Talos machinery packages for auth/config/client, not the full `talos` module command tree.
- No new major external dependencies beyond FUSE, Talos machinery, cosi runtime, and common stdlib/Cobra.
- The filesystem tree must tolerate multi-node targets by including a `nodes/<node>` segment when more than one node is targeted.
- Files are YAML by default; `--format json` switches file extensions and content to JSON.
- Writeback is opt-in and best-effort: only resources that are registered/parseable and that the API allows to be updated will succeed.

## Filesystem Layout

Talos resources are addressed by `(namespace, type, id)`. The filesystem mirrors this with an explicit namespace directory. When a resource type declares a default namespace (which is the common case), that namespace directory is still shown so that paths are unambiguous.

Single-node target:

```
<MOUNTPOINT>/
  <namespace>/
    <resource-type>/
      <id>.yaml
```

Multi-node target:

```
<MOUNTPOINT>/
  nodes/
    <node>/
      <namespace>/
        <resource-type>/
          <id>.yaml
```

Examples:
- `./default/links/eth0.yaml`
- `./network/addresses/eth0-192.168.1.10%2F24.yaml`
- `./nodes/192.168.1.10/default/links/eth0.yaml`

**Namespace handling**
- Directory names under the mount root (or under `nodes/<node>`) are namespace names as returned by `meta.ResourceDefinition.TypedSpec().DefaultNamespace` for the default namespace, or as explicitly used by the resource.
- When listing a resource type directory, all resources are fetched with the namespace inherited from the parent directory.
- The FUSE layer never guesses a namespace: it is always a path component.

**Resource type aliases**
- Directory names for resource types come from the first plural alias in `meta.ResourceDefinition.TypedSpec().Aliases` if available, otherwise the canonical type name.
- `Lookup` resolves any alias back to the canonical `ResourceDefinition` using `client.ResolveResourceKind`.
- IDs that are not filesystem-safe (e.g. contain `/`) are URL-escaped when used as file names (`%2F` for `/`). `Lookup` unescapes them before calling the API.

## Chunks

### Chunk 1: Module Skeleton and CLI

**Files Changed**
- `go.mod`
- `main.go`
- `cmd/root.go`

**Goal**
Create the Go module, wire a Cobra root command, and define all flags.

**Interface / Data**
```go
// cmd/root.go
var rootCmd = &cobra.Command{Use: "talos-fuse", ...}

func init() {
    rootCmd.Flags().StringP("mount", "m", "./", "mount point path")
    rootCmd.Flags().StringSliceP("nodes", "n", nil, "target nodes")
    rootCmd.Flags().StringSliceP("endpoints", "e", nil, "override endpoints")
    rootCmd.Flags().String("talosconfig", "", "talosconfig path")
    rootCmd.Flags().String("context", "", "talosconfig context")
    rootCmd.Flags().String("cluster", "", "cluster name")
    rootCmd.Flags().BoolP("maintenance", "i", false, "connect in maintenance mode")
    rootCmd.Flags().BoolP("write", "w", false, "enable two-way sync")
    rootCmd.Flags().String("format", "yaml", "file format: yaml or json")
    rootCmd.Flags().Bool("debug", false, "enable FUSE debug logging")
}
```

**Acceptance Criteria**
- `go build ./...` succeeds.
- `talos-fuse --help` lists all flags with defaults.
- Defaults mount point to `./`.

**Test Coverage**
- Unit test in `cmd/root_test.go` asserting flag parsing defaults and that `--write`/`--maintenance` boolean flags parse.

**Complexity**: simple

---

### Chunk 2: Talos Client and Resource Repository

**Files Changed**
- `internal/talos/client.go`
- `internal/talos/resources.go`

**Goal**
Encapsulate Talos connection setup, maintenance-mode TLS, node context injection, and CRUD-like operations against the COSI resource API. Also provide a helper to resolve the effective target node list from a loaded `talosconfig`.

**Interface / Data**
```go
package talos

import (
    "context"
    "github.com/cosi-project/runtime/pkg/resource"
    "github.com/cosi-project/runtime/pkg/resource/meta"
    "github.com/siderolabs/talos/pkg/machinery/client"
)

// Repository is the Talos resource backend.
type Repository struct {
    maintenance bool
    nodes       []string
    endpoints   []string
}

func NewRepository(maintenance bool, nodes, endpoints []string) *Repository

// WithClient runs action with a freshly constructed *client.Client.
func (r *Repository) WithClient(ctx context.Context, talosconfig, contextName, cluster string, action func(context.Context, *client.Client) error) error

// ResolveNodes loads the talosconfig/context and returns the effective nodes.
// If --nodes was provided, those are returned. Otherwise the configured context nodes are used.
func (r *Repository) ResolveNodes(talosconfig, contextName string) ([]string, error)

// ResourceDefinitions returns all resource definitions available to the current client.
func (r *Repository) ResourceDefinitions(ctx context.Context, c *client.Client) ([]*meta.ResourceDefinition, error)

// ResolveResourceKind resolves a resource type name or alias to its definition.
func (r *Repository) ResolveResourceKind(ctx context.Context, c *client.Client, namespace, kind string) (*meta.ResourceDefinition, error)

// ListResources returns all resources of a given definition/namespace for a node.
func (r *Repository) ListResources(ctx context.Context, c *client.Client, node, namespace string, rd *meta.ResourceDefinition) ([]resource.Resource, error)

// GetResource returns a single resource by ID.
func (r *Repository) GetResource(ctx context.Context, c *client.Client, node, namespace, id string, rd *meta.ResourceDefinition) (resource.Resource, error)

// UpdateResource writes a resource back to the API.
func (r *Repository) UpdateResource(ctx context.Context, c *client.Client, node string, rsrc resource.Resource) error
```

**Implementation Notes**
- For normal mode: open `clientconfig.Open(talosconfig)`, build `client.New` with `client.WithConfig`, context, endpoints, cluster as appropriate, and inject nodes with `client.WithNodes`.
- For maintenance mode: build a TLS config with `InsecureSkipVerify: true` (matching `talosctl --insecure`), endpoints from `--nodes`, and use `client.New(ctx, client.WithDefaultGRPCDialOptions(), client.WithTLSConfig(tlsConfig), client.WithEndpoints(...))`.
- Per-node calls use `client.WithNode(ctx, node)`.
- Use `state.WithSkipProtobufUnmarshal()` when reading so we get resources that `resource.MarshalYAML` can format.
- `ResourceDefinitions` uses `safe.StateListAll[*meta.ResourceDefinition]`.
- `ResolveNodes` uses `clientconfig.Open` + `cfg.Context(contextName)` + `context.Nodes`. If still empty, return an error telling the user to set `--nodes`.

**Acceptance Criteria**
- `go test ./internal/talos/...` passes.
- Repository can be constructed with maintenance and non-maintenance settings.
- `ResolveNodes` returns CLI-provided nodes unchanged and can load context nodes from a test talosconfig file.

**Test Coverage**
- `internal/talos/client_test.go`: construction tests, node resolution tests with a fixture talosconfig.
- `internal/talos/resources_test.go`: fake repository tests? Not needed if covered by FUSE tests.

**Complexity**: simple

---

### Chunk 3a: Read-only FUSE Tree

**Files Changed**
- `internal/fs/talosfs.go`
- `internal/fs/nodes.go`
- `internal/fs/format.go`

**Goal**
Implement the FUSE tree: root, optional node directory, namespace directory, resource type directory, and resource file read path.

**Interface / Data**
```go
package fs

import (
    "context"

    gfuse "github.com/hanwen/go-fuse/v2/fs"
    "github.com/hanwen/go-fuse/v2/fuse"
)

// Options configure the filesystem.
type Options struct {
    Repository     ResourceRepository
    Maintenance    bool
    Writeable      bool
    Format         string // "yaml" or "json"
    Nodes          []string
}

// ResourceRepository abstracts the backend for filesystem tests.
type ResourceRepository interface {
    ResourceDefinitions(ctx context.Context) ([]*meta.ResourceDefinition, error)
    ListResources(ctx context.Context, node, namespace, resourceType string) ([]resource.Resource, error)
    GetResource(ctx context.Context, node, namespace, resourceType, id string) (resource.Resource, error)
    UpdateResource(ctx context.Context, node string, rsrc resource.Resource) error
}

// TalosRoot is the FUSE root node.
type TalosRoot struct {
    gfuse.Inode
    opts Options
}
```

**Node Types**
- `TalosRoot` — `Getattr`, `Lookup`, `Readdir`. If `len(opts.Nodes) > 1` it exposes `nodes` and direct namespace children? No: direct children are either `nodes` (multi-node) or namespaces (single-node).
- `nodeDir` (created only when multi-node) — `Getattr`, `Lookup`, `Readdir`; children are namespaces.
- `namespaceDir` — knows namespace name; children are resource type directories derived from `ResourceDefinitions`.
- `resourceTypeDir` — knows namespace and resource type (canonical type and display alias); `Lookup` resolves escaped `<id>.ext`; `Readdir` lists resources.
- `resourceFile` — knows node, namespace, resource type, id; `Getattr`, `Open`, `Read` returns formatted content.

**Formatting**
- Use `resource.MarshalYAML(r)` then encode to YAML or JSON.
- YAML file extension `.yaml`, JSON extension `.json`.
- JSON output is produced by marshaling the intermediate `any` value from `resource.MarshalYAML` with `encoding/json`.

**Acceptance Criteria**
- `go test ./internal/fs/...` passes.
- A stub repository can be mounted and read in-process using go-fuse test helpers.
- Read-only mount rejects writes with `EACCES`.
- JSON format produces `.json` files and valid JSON content.

**Test Coverage**
- `internal/fs/talosfs_test.go` with a fake `ResourceRepository`.
- Tests for lookup, readdir, read YAML content, read JSON content, and write rejection in read-only mode.

**Complexity**: complex

---

### Chunk 3b: Writeback File Handles

**Files Changed**
- `internal/fs/file.go`
- `internal/fs/writeback.go`

**Goal**
When `Options.Writeable` is true, allow writing resource files and flush the modified content back to the COSI API.

**Interface / Data**
```go
package fs

// fileHandle holds the write buffer for an open resource file.
type fileHandle struct {
    content []byte
    node    *resourceFile
}

// Flush implements the writeback.
func (fh *fileHandle) Flush(ctx context.Context) syscall.Errno
```

**Write Path**
- `resourceFile` allocates a `fileHandle` with a buffer when opened for write.
- `Write` appends to the buffer.
- `Flush`:
  1. If maintenance mode is enabled, reject with `EROFS`.
  2. If format is JSON, convert the buffer to YAML first.
  3. Use `resourceutil.ParseResource` to build a `*protobuf.Resource` from the document.
  4. Validate that the parsed resource metadata matches the file path (namespace, type, id).
  5. Call `Repository.UpdateResource(ctx, node, parsed)`.
  6. On failure return an appropriate errno: `EINVAL` for parse/validation errors, `EPERM` for permission/read-only resource errors, `EIO` for unexpected API errors.

**Acceptance Criteria**
- `go test ./internal/fs/...` passes.
- Write-enabled mount accepts writes and calls `UpdateResource` with the parsed resource on flush.
- Maintenance mode rejects writes with `EROFS`.
- Parse/type/id mismatch returns `EINVAL`.

**Test Coverage**
- `internal/fs/writeback_test.go` with a fake `ResourceRepository`.
- Tests for successful YAML writeback, JSON writeback, id mismatch, maintenance mode rejection, and read-only mount rejection.

**Complexity**: complex

---

### Chunk 4: Adapter and Main Mount Loop

**Files Changed**
- `internal/fs/adapter.go`
- `main.go` (updated)

**Goal**
Glue the repository from Chunk 2 into the `ResourceRepository` interface from Chunk 3, mount the FUSE server, and block until unmount.

**Interface / Data**
```go
package fs

import (
    "context"
    "github.com/siderolabs/talos/pkg/machinery/client"
)

// RepositoryAdapter wraps the Chunk 2 repository so it satisfies ResourceRepository.
type RepositoryAdapter struct {
    Repo         *talos.Repository
    Talosconfig  string
    ContextName  string
    Cluster      string
}

func (a *RepositoryAdapter) ResourceDefinitions(ctx context.Context) ([]*meta.ResourceDefinition, error) {
    var result []*meta.ResourceDefinition
    err := a.Repo.WithClient(ctx, a.Talosconfig, a.ContextName, a.Cluster, func(ctx context.Context, c *client.Client) error {
        defs, err := a.Repo.ResourceDefinitions(ctx, c)
        result = defs
        return err
    })
    return result, err
}

// ... similar for ListResources, GetResource, UpdateResource
```

**Main Mount Loop**
- Parse flags.
- Build `Repository` and resolve nodes via `ResolveNodes`.
- Build `RepositoryAdapter`.
- Build `fs.Options` with resolved nodes.
- Call `fs.Mount(mountpoint, &TalosRoot{opts: opts}, &fs.Options{MountOptions: fuse.MountOptions{Name: "talos-fuse", Debug: debug}, ...})`.
- Wait for `server.Wait()`.

**Acceptance Criteria**
- `go build ./...` succeeds.
- Mount helper test verifies the FUSE server can be created and unmounted in-process.

**Test Coverage**
- `cmd/integration_test.go` or `main_test.go`: compile-level smoke test.

**Complexity**: simple

---

### Chunk 5: Resource YAML/JSON Conversion Helper

**Files Changed**
- `internal/resourceutil/format.go`
- `internal/resourceutil/parse.go`
- `internal/resourceutil/resourceutil_test.go`

**Goal**
Provide formatting for reads and generic parsing for writeback that does not rely on the COSI static resource registry (Talos resources are registered dynamically and cannot be created via `protobuf.CreateResource`).

**Implementation Notes**
- `FormatResource(r resource.Resource, format string) ([]byte, error)` uses `resource.MarshalYAML(r)` and encodes the result as YAML or JSON.
- `ParseResource(data []byte, format string) (*protobuf.Resource, error)` parses a YAML/JSON document, extracts the `metadata` and `spec` nodes, uses `resource.Metadata.UnmarshalYAML` for metadata, marshals the `spec` node back to YAML bytes, builds a `cosi.resource.Resource` protobuf message with `Spec.YamlSpec` set, and finally uses `protobuf.Unmarshal` to produce a `*protobuf.Resource`.
- This generic path works for any Talos resource because the server side parses `YamlSpec`; the client never needs to materialize the typed spec.

**Acceptance Criteria**
- `go build ./internal/resourceutil/...` succeeds.
- `go test ./internal/resourceutil/...` passes.
- A known YAML document round-trips through `ParseResource` and `FormatResource` producing equivalent content.

**Test Coverage**
- `internal/resourceutil/resourceutil_test.go` with round-trip tests for YAML and JSON, plus metadata/spec extraction tests.

**Complexity**: simple

---

## Verification Strategy

1. **Static checks**: `go build ./...`, `go vet ./...`, `gofmt -l`.
2. **Unit tests**: `go test ./...`.
3. **In-process FUSE test**: use go-fuse test harness with a stub repository.
4. **Manual smoke test** (documented in README):
   - Build `talos-fuse`.
   - Mount against a Talos node: `talos-fuse -n 10.0.0.2 /tmp/talos`.
   - `ls /tmp/talos` shows namespace directories.
   - `cat /tmp/talos/default/links/eth0.yaml` returns YAML.
   - Maintenance mode: `talos-fuse -i -n 10.0.0.2 /tmp/talos-maint`.

## Decision Log

- **Use Talos machinery instead of importing cmd/talosctl**: Importing `cmd/talosctl` pulls the entire Talos module and its heavy dependencies. The machinery client/config packages provide the same authentication behavior used by talosctl.
- **go-fuse v2 over bazil.org/fuse**: go-fuse v2 is actively maintained, has a cleaner inode API, and works on Linux and macOS.
- **YAML default**: Matches `talosctl get -o yaml`.
- **Explicit namespace directories**: Talos resources are keyed by `(namespace, type, id)`. Surfacing namespaces as directories avoids collisions and keeps paths unambiguous.
- **Multi-node layout**: Inserting a `nodes/<node>` directory only when needed keeps the common single-node case simple.
- **Alias directories**: Resource type directories use the plural alias when available because that is what users type with `talosctl get`.
- **Writeback via `protobuf.YAMLResource`**: COSI provides a YAML unmarshaler that creates typed resources from the global registry. We blank-import Talos resource packages to populate that registry.
- **Maintenance mode is read-only**: The maintenance API only grants `os:reader` access; writes are rejected at the FUSE layer with `EROFS`.
- **No caching**: Each FUSE operation calls the API. This keeps correctness straightforward; performance can be improved later if needed.
- **No streaming/etcd support**: Explicitly excluded by the request.

## Risks

- **Dependency weight**: `pkg/machinery` still pulls a lot of indirect dependencies. We accept this because Talos resource types and client auth require it.
- **Resource registration failures**: Some Talos resource packages may have Linux-specific imports that fail on non-Linux builds. The plan includes build-tag fallbacks.
- **Writeback limitations**: Not all resources are mutable, and the server may reject updates. Errors are surfaced to the user as write errors; the tool will not attempt partial/merge patches.
- **Maintenance-mode resource differences**: The filesystem simply exposes whatever `ResourceDefinitions` and `COSI.List/Get` return in maintenance mode; no hardcoded allow-list is maintained.
