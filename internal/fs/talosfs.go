// Package fs implements the FUSE filesystem exposing Talos COSI
// resources as a directory tree. The tree layout is:
//
//	/<cluster>/<node>/<namespace>/<resource-type>/<id>.<ext>
//
// where <cluster> is Options.Cluster when set, or "nodes" when unset.
//
// The filesystem is read-only by default. When Options.Writeable is
// true the file handles become writable and flush back through
// ResourceRepository.UpdateResource (see Chunk 3b).
package fs

import (
	"context"
	"syscall"

	gfuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
)

// Options configure the filesystem.
type Options struct {
	// Repository is the resource backend.
	Repository ResourceRepository
	// Maintenance indicates the maintenance mode; when true the file
	// system is read-only regardless of Writeable.
	Maintenance bool
	// Writeable allows writes to flow back to the API. Always false
	// when Maintenance is true.
	Writeable bool
	// Format is "yaml" or "json"; controls file extension and content.
	Format string
	// Nodes is the set of target nodes.
	Nodes []string
	// Cluster, when non-empty, is used as the name of the top-level
	// directory in the mounted tree instead of the default "nodes".
	Cluster string
	// NodeLabels maps each node ID (UUID or IP) to a human-readable
	// display name used as the directory name in the tree. When a node
	// has no entry here its ID is used unchanged. COSI calls always use
	// the original ID regardless of the label.
	NodeLabels map[string]string
	// MountOptions, when non-zero, provides FUSE mount options that
	// override the defaults (notably the Debug flag and FsName).
	MountOptions fuse.MountOptions
}

// ResourceRepository abstracts the backend so the FUSE layer can be
// unit tested with a fake implementation.
type ResourceRepository interface {
	// ResourceDefinitions returns the resource definitions advertised
	// by the given node. node must be non-empty when the backend routes
	// through a proxy (e.g. Omni) that requires per-node addressing.
	ResourceDefinitions(ctx context.Context, node string) ([]*meta.ResourceDefinition, error)
	// ListResources returns the resources of resourceType under
	// namespace on node. node may be empty to address the multi-node
	// repository.
	ListResources(ctx context.Context, node, namespace, resourceType string) ([]resource.Resource, error)
	// GetResource returns a single resource by id under namespace on
	// node.
	GetResource(ctx context.Context, node, namespace, resourceType, id string) (resource.Resource, error)
	// UpdateResource writes the resource back to the API.
	UpdateResource(ctx context.Context, node string, rsrc resource.Resource) error
}

// TalosRoot is the FUSE root inode for the talos-fuse filesystem.
//
// The root always exposes a "nodes" directory whose children are the
// per-node directories for each configured node.
type TalosRoot struct {
	gfuse.Inode
	opts Options
}

// Mount wires up the FUSE server and starts serving.
//
// dir is the mountpoint; root is the filesystem root (typically a
// fresh &TalosRoot{}); opts is the filesystem options. Callers are
// responsible for invoking server.Wait() and server.Unmount().
func Mount(dir string, root *TalosRoot, opts *Options) (*fuse.Server, error) {
	root.opts = *opts

	mo := opts.MountOptions
	if mo.Name == "" {
		mo.Name = "talos-fuse"
	}
	if mo.FsName == "" {
		mo.FsName = "talos-fuse"
	}

	// Preserve the read-only / default_permissions policy unless the
	// caller explicitly supplied their own -o string.
	if len(mo.Options) == 0 {
		if opts.Writeable {
			// Read-write mount is allowed only outside maintenance mode.
			mo.Options = []string{"default_permissions"}
		} else {
			mo.Options = []string{"ro"}
		}
	}

	fsOpts := &gfuse.Options{
		MountOptions: mo,
		// Report files as owned by the mounting user. The FUSE bridge
		// defaults Uid/Gid to 0; without this override the kernel
		// would treat the test process (uid != 0) as a non-owner and
		// refuse writes even on writable mounts.
		UID: uint32(syscall.Getuid()),
		GID: uint32(syscall.Getgid()),
	}

	return gfuse.Mount(dir, root, fsOpts)
}
