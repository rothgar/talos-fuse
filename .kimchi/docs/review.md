# talos-fuse Review

## Verdict: NEEDS_FIXES

The implementation builds cleanly and the unit/in-process FUSE tests pass, but the multi-node filesystem layout required by the plan is not implemented correctly. There are also missing input validations and a deviation from the specified writeback path.

## Issues

### 1. Multi-node layout is broken

**File:** `internal/fs/nodes.go`

The plan requires the multi-node layout:

```
<MOUNTPOINT>/
  nodes/
    <node>/
      <namespace>/
        <resource-type>/
          <id>.yaml
```

The current `nodeDir` type is used for the `/nodes` directory but it behaves like a per-node namespace directory instead of a directory of node directories.

**Line 98-121:** `nodeDir.Readdir` lists namespaces from `ResourceDefinitions` instead of listing the configured node names:

```go
func (n *nodeDir) Readdir(ctx context.Context) (gfuse.DirStream, syscall.Errno) {
    ...
    names := namespaceNames(defs)   // should be n.nodes
    ...
}
```

**Line 79-95:** `nodeDir.Lookup` resolves a namespace name and creates a `namespaceDir` whose `node` field is set to the namespace name instead of the actual target node:

```go
child := n.NewInode(ctx, &namespaceDir{opts: n.opts, namespace: name, defs: defs, node: name}, stable)
```

Because `name` here is the looked-up directory entry (a namespace name), every per-node path ends up with `node == <namespace>` rather than the real node. `ListResources`/`GetResource` then receive the wrong node value.

**Suggested fix:** Make `nodeDir` a true `/nodes` container: `Readdir` should return entries for `n.nodes`, and `Lookup` should validate that `name` is one of `n.nodes` and create a child directory whose children are namespaces and whose `node` field is the matched node name. Introduce a new node type for the per-node directory if necessary (e.g. `perNodeDir`), or reuse `namespaceDir` under a node-named parent.

### 2. No validation of the `--format` flag

**File:** `cmd/root.go`

The plan states that files are YAML by default and `--format json` switches file extensions and content to JSON. The flag accepts any string, so an invalid value such as `--format toml` is passed through to the FUSE layer unchanged.

`internal/fs/format.go:normalizeFormat` silently falls back to `yaml`, but `resourceutil.FormatResource` then returns an error for unsupported formats, causing all file reads to fail with `EIO` and giving the user no useful feedback.

**Suggested fix:** In `RunE`, validate `format` against the allowed set `{"yaml", "json"}` (case-insensitively) and return a clear error before mounting if it is invalid.

### 3. JSON writeback does not convert to YAML first

**File:** `internal/fs/file.go`

The plan specifies that when the file format is JSON, the writeback path should "convert the buffer to YAML first" before parsing. The current `Flush` path passes the JSON buffer directly to `resourceutil.ParseResource` with `format == "json"`:

```go
format := normalizeFormat(h.file.opts.Format)
parsed, err := resourceutil.ParseResource(data, format)
```

While `yaml.Unmarshal` happens to parse JSON, this is a deviation from the spec and may mask encoding-specific edge cases. It also means the `--format json` write path relies on YAML parsing rather than an explicit JSON-to-YAML conversion.

**Suggested fix:** When `format == "json"`, translate the JSON document to YAML bytes before calling `ParseResource`, or parse JSON explicitly and then marshal the `metadata`/`spec` subtrees to YAML for the protobuf `YamlSpec` field.

### 4. Missing manual smoke-test documentation

**File:** n/a (missing `README.md`)

The plan's Verification Strategy includes a "Manual smoke test (documented in README)" with commands for normal and maintenance mounts. No `README.md` is present in the repository.

**Suggested fix:** Add a `README.md` documenting build instructions, the required `--nodes` / `--maintenance` flags, and the manual smoke-test commands described in the plan.

## Checks Run

```text
$ go build ./...
$ go vet ./...
$ go test ./...
```

All succeeded.

```text
$ gofmt -l .
```

No formatting issues.
