# Plan Review: talos-fuse

## Verdict: NEEDS_REVISION

The plan captures the high-level shape of the CLI, FUSE layout, Talos client, and writeback path, but several concrete design points are missing or underspecified. The biggest blockers are the filesystem namespace model, node resolution from `talosconfig`, and an overloaded FUSE chunk that mixes read-only tree construction with writeback.

## Specific gaps and recommended revisions

1. **Filesystem layout ignores namespaces (core requirement gap)**
   - Reference: Filesystem Layout section, Chunk 3 `internal/fs/talosfs.go`, Chunk 3 `ResourceRepository` interface.
   - Issue: Talos COSI resources live in namespaces (e.g. `default`, `network`, `config`). The proposed tree is `<resource-type>/<id>.yaml` with no namespace segment, yet `ResourceRepository.ListResources`/`GetResource` take a `namespace string` parameter. The plan never says which namespace is used, how non-default namespaces are surfaced, or how collisions between two resources of the same type/id in different namespaces are avoided.
   - Suggested fix: Decide whether (a) namespaces appear as an intermediate directory (`<namespace>/<resource-type>/<id>.yaml`) or (b) each resource type is implicitly scoped to its default namespace. Document the choice in the Filesystem Layout section and update the node type definitions accordingly.

2. **No design for resolving target nodes from `talosconfig`**
   - Reference: Chunk 1 `cmd/root.go` flags, Chunk 2 `internal/talos/client.go`, Chunk 4 `main.go`.
   - Issue: `--nodes` defaults to `nil`, but the multi-node layout depends on knowing whether one or many nodes are targeted. The plan never describes how nodes are read from the selected `talosconfig` context when `--nodes` is omitted. Without this, `len(opts.Nodes) > 1` cannot be evaluated and the `nodes/<node>` segment cannot be built correctly.
   - Suggested fix: Add a step in Chunk 2 or Chunk 4 to load `clientconfig.Open(talosconfig)`, select the context, and extract `Context.Nodes` when no `--nodes` override is provided. Pass the resolved slice to `fs.Options`.

3. **Resource type directory naming / aliases are unspecified**
   - Reference: Filesystem Layout section, Chunk 3 `resourceTypeDir`.
   - Issue: Talos resource definitions expose a type and multiple aliases (e.g. `LinkStatus` may also be reachable as `links`). The plan mentions `ResolveResourceKind` in Chunk 2 but does not say which name is used for the directory, whether aliases are accepted in paths, or how `Readdir` enumerates resource types.
   - Suggested fix: State that directories are created from the plural alias used by `talosctl get` and that `Lookup` must resolve aliases back to the canonical `meta.ResourceDefinition`.

4. **FUSE chunk is overloaded; writeback should be split out**
   - Reference: Chunk 3 (`internal/fs/talosfs.go`, `internal/fs/nodes.go`, `internal/fs/file.go`).
   - Issue: Chunk 3 covers root/node/type directory nodes, read-only reads, file formatting, AND writable file handles, flush, YAML/JSON conversion, validation, and update. This is the only chunk marked complex and contains two distinct technical problems: FUSE inode tree semantics and safe writeback parsing.
   - Suggested fix: Split Chunk 3 into:
     - **Chunk 3a: Read-only FUSE tree** (root, nodeDir, resourceTypeDir, resourceFile read path, YAML/JSON formatting).
     - **Chunk 3b: Writeback file handles** (writable `fileHandle`, buffer writes, JSON-to-YAML conversion, `protobuf.YAMLResource` parsing, type/id validation, `UpdateResource` flush, EROFS/EACCES mapping).

5. **Maintenance-mode connection design is hand-wavy**
   - Reference: Chunk 2 `internal/talos/client.go`, Constraints section.
   - Issue: The plan builds a custom TLS config with `InsecureSkipVerify: true` and endpoints from `--nodes`. Talos machinery already provides maintenance-mode client constructors/options; rolling a custom TLS path risks auth/connection failures and may not match `talosctl`'s behavior.
   - Suggested fix: Either explicitly state which machinery helper/options will be used for maintenance mode, or document why a custom TLS config is necessary and how port/endpoints are derived.

6. **`ResourceRepository` namespace parameter has no caller semantics**
   - Reference: Chunk 3 `ResourceRepository` interface.
   - Issue: The interface passes `namespace string` to `ListResources`/`GetResource`, but without a namespace-aware filesystem layout it is unclear what value the FUSE layer will supply. Passing `""` or `"default"` for every call will hide namespaced resources.
   - Suggested fix: Define how the FUSE layer resolves a namespace for each resource type directory, and remove the parameter if it is not actually used.

7. **Writeback edge cases and error behavior are underspecified**
   - Reference: Chunk 3 `internal/fs/file.go`, Write Path subsection.
   - Issue: The plan validates that parsed type/id matches the file path but does not address: read-only COSI resources that reject updates, writes in maintenance mode, atomicity/retry on conflict, or which FUSE error codes are returned for parse/validation/update failures.
   - Suggested fix: Add acceptance criteria for rejecting updates on read-only resources (e.g. `EPERM`), for disabling/ignoring writes in maintenance mode, and for mapping `UpdateResource` errors to `EIO`/`EINVAL`.

8. **Dependency version may be invalid**
   - Reference: Constraints section (`github.com/siderolabs/talos/pkg/machinery@v1.13.7`).
   - Issue: Talos machinery release versions do not currently include `v1.13.7` (latest stable releases are in the `v1.9`/`v1.10` range depending on date). A non-existent version will break module resolution on the first build.
   - Suggested fix: Replace with a verified, existing machinery version and confirm its minimum Go version. Update the Go 1.23+ compatibility claim accordingly.

9. **Missing test coverage for JSON round-trip and maintenance mode**
   - Reference: Verification Strategy, Chunk 3 test coverage.
   - Issue: Tests are planned for lookup, readdir, read content, and write-flush round-trip, but not for JSON format file extension/content, maintenance-mode filesystem layout, or the read-only mount rejecting writes.
   - Suggested fix: Add explicit test acceptance criteria for `--format json` reads/writes and for `EACCES`/`EROFS` behavior when `--write` is false.

## Summary

Approve the overall direction once the plan is revised to define namespace handling, node resolution from config, resource alias mapping, and once the FUSE/writeback chunk is split. Also verify the Talos machinery version before implementation begins.
