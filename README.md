# talos-fuse

`talos-fuse` exposes Talos API resources as a FUSE filesystem. Resources are
addressed by `(namespace, type, id)` and rendered as files. The mount is
read-only by default; with `--sync`, modifications to resource files are
parsed and pushed back through the COSI resource API.

## Installation

### Homebrew

```sh
brew tap rothgar/tap
brew install talos-fuse
```

### Build from source

```sh
go build ./...
```

The resulting binary is `./talos-fuse`.

## Usage

### Single node

Mount against a node referenced in your `talosconfig`:

```sh
talos-fuse \
  --talosconfig $HOME/.talos/config \
  --context my-cluster \
  --nodes 10.0.0.2 \
  --mount ./mnt
```

The filesystem uses a `nodes/<hostname>/` layout regardless of how many
nodes are targeted. Node UUIDs are resolved to hostnames automatically; if
resolution fails the UUID is used instead:

```
mnt/
  nodes/
    my-node/
      default/
        linkstatuses/
          eth0.yaml
          eth1.yaml
        ...
      cluster/
        memberstatuses/
          my-node.yaml
        ...
      network/
        ...
```

### Multi-node

Pass multiple nodes via repeated or comma-separated `--nodes`. Each node
gets its own directory under `nodes/`:

```sh
talos-fuse \
  --talosconfig $HOME/.talos/config \
  --context my-cluster \
  --nodes node-a,node-b,node-c \
  --mount ./mnt
```

```
mnt/
  nodes/
    node-a/
      ...
    node-b/
      ...
    node-c/
      ...
```

### Omni-managed clusters

For clusters managed by Omni, supply the cluster name via `--cluster`.
The context's `cluster:` field is used for request routing through the
Omni proxy; you do not need to supply `--nodes` separately when the
cluster nodes are enumerated through Omni:

```sh
talos-fuse \
  --talosconfig $HOME/.talos/config \
  --context omni-context \
  --cluster my-omni-cluster \
  --nodes 10.0.0.1,10.0.0.2 \
  --mount ./mnt
```

`--sync` is blocked for Omni-managed clusters because Omni reconciles
machine config through its own patch system; direct COSI writes would be
silently reverted on the next reconcile cycle.

### Maintenance mode

Maintenance mode uses insecure TLS to connect to a node that has not yet
been provisioned. The filesystem is always read-only in this mode:

```sh
talos-fuse \
  --nodes 10.0.0.2 \
  --endpoints https://10.0.0.2:50000 \
  --maintenance \
  --mount ./mnt
```

### Write-back (--sync)

With `--sync`, writing a modified YAML or JSON file back to the filesystem
pushes the change through the COSI resource API:

```sh
talos-fuse \
  --talosconfig $HOME/.talos/config \
  --context my-cluster \
  --nodes 10.0.0.2 \
  --sync \
  --mount ./mnt

# edit and save
$EDITOR mnt/nodes/my-node/network/addressstatuses/eth0.yaml
```

The file is validated against the resource metadata before the write is
sent; mismatches in namespace, type, or ID return `EINVAL`.

### Resource ID escaping

Resource IDs that contain a `/` character (common in Kubernetes-style
names) are rendered with `/` replaced by `+` in filenames. For example, a
resource with ID `kube-system/coredns` appears as
`kube-system+coredns.yaml`. This is reversed transparently when the path
is resolved.

## Flags

| Flag            | Short | Default  | Description                                                  |
|-----------------|-------|----------|--------------------------------------------------------------|
| `--mount`       | `-m`  | `./`     | Mount point path                                             |
| `--nodes`       | `-n`  | _none_   | Target nodes, comma-separated                                |
| `--endpoints`   | `-e`  | _none_   | Override endpoints, comma-separated                          |
| `--talosconfig` |       | _none_   | Path to `talosconfig` (defaults to `$HOME/.talos/config`)   |
| `--context`     |       | _none_   | `talosconfig` context name (defaults to current context)     |
| `--cluster`     |       | _none_   | Cluster name (required for Omni-managed clusters)            |
| `--maintenance` | `-i`  | `false`  | Connect in maintenance mode (insecure TLS)                   |
| `--sync`        | `-s`  | `false`  | Enable two-way sync; resource writes flush back via COSI     |
| `--format`      |       | `yaml`   | File format: `yaml` or `json`                                |
| `--cache-ttl`   |       | `5m`     | TTL for cached resource definitions and listings             |
| `--debug`       |       | `false`  | Enable FUSE debug logging                                    |

`--sync` is implicitly disabled in maintenance mode and blocked for
Omni-managed clusters.

## Tests

```sh
go test ./...
```

Unit tests cover the FUSE tree structure, multi-node layout, resource ID
escaping, write-back validation, TTL caching, and Omni context detection.
Integration tests (prefixed `TestMount_`) mount a real in-process FUSE
filesystem and exercise end-to-end read and write paths.
