# talos-fuse

`talos-fuse` exposes Talos API resources as a FUSE filesystem. Resources are
addressed by `(namespace, type, id)` and rendered as files. The mount is
read-only by default; with `--sync`, modifications to resource files are
parsed and pushed back through the COSI resource API.

## Build

```sh
go build ./...
```

The resulting binary lives at `./talos-fuse`.

## Usage

### Normal mode

Mount a single-node filesystem against a Talos node referenced in your
`talosconfig`:

```sh
talos-fuse \
  --talosconfig $HOME/.talos/config \
  --context my-cluster \
  --nodes 10.0.0.2 \
  --mount ./mnt
```

The mount point will expose:

```
mnt/
  nodes/
    node-a/
      default/
        linksstatus/
          eth0.yaml
        ...
      cluster/
        memberstatuses/
          ...
```

### Maintenance mode

Maintenance mode uses insecure TLS to connect to a node that has booted
without an identity. The filesystem is always read-only in this mode:

```sh
talos-fuse \
  --nodes 10.0.0.2 \
  --endpoints https://10.0.0.2:50000 \
  --maintenance \
  --mount ./mnt
```

## Multi-node layout

The filesystem always uses a `/nodes/<node>/` intermediate directory,
regardless of how many nodes are targeted. With a single node the layout
is:

```
mnt/
  nodes/
    node-a/
      default/
        linksstatus/
          eth0.yaml
      cluster/
        memberstatuses/
          ...
```

When more than one node is supplied via `--nodes`, the `/nodes`
directory simply contains one entry per node:

```
mnt/
  nodes/
    node-a/
      ...
    node-b/
      ...
```

Each per-node directory is independent; the node name is forwarded to the
COSI repository so that listings and reads are scoped to the right node.

## Flags

| Flag              | Shorthand | Default | Description                                       |
|-------------------|-----------|---------|---------------------------------------------------|
| `--mount`         | `-m`      | `./`    | Mount point path                                  |
| `--nodes`         | `-n`      | _none_  | Target nodes, comma-separated                     |
| `--endpoints`     | `-e`      | _none_  | Override endpoints, comma-separated               |
| `--talosconfig`   |           | _none_  | Path to `talosconfig`                             |
| `--context`       |           | _none_  | `talosconfig` context name                        |
| `--cluster`       |           | _none_  | Cluster name                                      |
| `--maintenance`   | `-i`      | `false` | Connect in maintenance mode (insecure TLS)        |
| `--sync`          | `-s`      | `false` | Enable two-way sync; resource writes flush back   |
| `--format`        |           | `yaml`  | File format: `yaml` or `json`                     |
| `--debug`         |           | `false` | Enable FUSE debug logging                         |

`--sync` is implicitly disabled in maintenance mode.

## Tests

```sh
go test ./...
```
