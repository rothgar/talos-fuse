# Plan Review: talos-fuse (Revised)

## Verdict: APPROVED

The revised plan resolves all nine issues raised in the previous review.

1. **Filesystem layout includes Talos namespaces** — The tree is now explicitly `<namespace>/<resource-type>/<id>.yaml` for single-node targets and `nodes/<node>/<namespace>/<resource-type>/<id>.yaml` for multi-node targets. The "Namespace handling" subsection makes namespaces unambiguous path components.

2. **Node resolution from talosconfig when `--nodes` is omitted** — `Repository.ResolveNodes` now loads the selected `talosconfig` context and returns `context.Nodes` when no `--nodes` override is supplied, with an explicit error if nodes remain empty.

3. **Resource type alias vs directory naming** — The "Resource type aliases" subsection specifies that directory names use the first plural alias and that `Lookup` resolves aliases back to the canonical `ResourceDefinition` via `client.ResolveResourceKind`.

4. **FUSE chunk split into read-only tree and writeback** — Chunk 3 is now cleanly separated into Chunk 3a (read-only FUSE tree) and Chunk 3b (writeback file handles).

5. **Maintenance-mode connection design** — The design is now explicit: custom TLS config with `InsecureSkipVerify: true`, endpoints from `--nodes`, and `client.New` with `client.WithDefaultGRPCDialOptions`, `client.WithTLSConfig`, and `client.WithEndpoints`. A check of `github.com/siderolabs/talos/pkg/machinery@v1.13.7` confirms there is no dedicated maintenance-mode client option in the machinery client, so the custom TLS path is the appropriate machinery-level approach.

6. **Namespace parameter semantics in `ResourceRepository`** — The interface retains the `namespace` parameter, and the namespace-aware layout now defines exactly what value the FUSE layer will supply (the parent directory name).

7. **Writeback edge cases and error behavior** — Chunk 3b now maps failures concretely: parse/validation errors to `EINVAL`, permission/read-only resource errors to `EPERM`, unexpected API errors to `EIO`, maintenance-mode writes to `EROFS`, and read-only mounts to `EACCES`.

8. **Dependency version validity** — `github.com/siderolabs/talos/pkg/machinery@v1.13.7` exists and its `go.mod` requires Go 1.26.5, which is consistent with the plan's Go 1.26+ requirement and the installed toolchain.

9. **Missing test coverage** — Acceptance criteria and test coverage now explicitly include JSON format reads/writes, read-only mount rejection (`EACCES`), and maintenance-mode write rejection (`EROFS`).

The plan is ready for implementation.
