package fs

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	gfuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"

	"github.com/jgarr/talos-fuse/internal/resourceutil"
)

// dirMode is the mode used for directories: 0755.
const dirMode = 0o755

// fileModeRO is the mode used for read-only files: 0444.
const fileModeRO = 0o444

// fileModeRW is the mode used for writable files: 0644.
const fileModeRW = 0o644

// dirTimeout is the attribute/entry timeout for directory inodes. The
// kernel caches namespace and resource-type listings for this duration,
// avoiding a FUSE round-trip on every stat or traversal step.
var dirTimeout = 30 * time.Second

// fileAttrTimeout is the attribute timeout for file inodes. Kept short
// so that file sizes reported by stat stay reasonably fresh; content is
// fetched eagerly at Open time so reads are unaffected.
var fileAttrTimeout = 5 * time.Second

// ----- TalosRoot (root directory) -----

// Getattr implements fs.NodeGetattrer.
func (r *TalosRoot) Getattr(_ context.Context, _ gfuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = dirMode
	out.SetTimeout(dirTimeout)
	return 0
}

// topLevelName returns the name of the single top-level directory
// exposed by the root. When a cluster name is configured it is used
// so that the tree reads as /<cluster>/<node>/...; otherwise the
// legacy "nodes" name is used.
func (r *TalosRoot) topLevelName() string {
	if r.opts.Cluster != "" {
		return r.opts.Cluster
	}
	return "nodes"
}

// Lookup implements fs.NodeLookuper. The root always contains a single
// directory (the cluster name or "nodes") whose children are the
// per-node directories.
func (r *TalosRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gfuse.Inode, syscall.Errno) {
	if name != r.topLevelName() {
		return nil, syscall.ENOENT
	}

	stable := gfuse.StableAttr{Mode: fuse.S_IFDIR}
	child := r.NewInode(ctx, &nodeDir{
		opts:   r.opts,
		nodes:  append([]string(nil), r.opts.Nodes...),
		labels: buildNodeLabels(r.opts.Nodes, r.opts.NodeLabels),
	}, stable)
	out.Attr.Mode = dirMode
	out.SetAttrTimeout(dirTimeout)
	out.SetEntryTimeout(dirTimeout)

	return child, 0
}

// Readdir implements fs.NodeReaddirer. The root always contains a
// single directory entry (the cluster name or "nodes").
func (r *TalosRoot) Readdir(_ context.Context) (gfuse.DirStream, syscall.Errno) {
	return gfuse.NewListDirStream([]fuse.DirEntry{
		{Name: r.topLevelName(), Mode: fuse.S_IFDIR},
	}), 0
}

// ----- nodeDir (/nodes directory) -----

// nodeDir is the /nodes directory. Its children are per-node
// directories, one for each configured node.
type nodeDir struct {
	gfuse.Inode
	opts   Options
	nodes  []string          // node IDs in original order (used for routing)
	labels map[string]string // display name → node ID
}

// buildNodeLabels constructs the display-name → node-ID map for nodeDir.
// When nodeLabels maps a node ID to a non-empty label that label is used
// as the directory name; otherwise the node ID is used unchanged.
func buildNodeLabels(nodes []string, nodeLabels map[string]string) map[string]string {
	m := make(map[string]string, len(nodes))
	for _, id := range nodes {
		name := id
		if nodeLabels != nil {
			if label, ok := nodeLabels[id]; ok && label != "" {
				name = label
			}
		}
		m[name] = id
	}
	return m
}

// Getattr implements fs.NodeGetattrer.
func (n *nodeDir) Getattr(_ context.Context, _ gfuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = dirMode
	out.SetTimeout(dirTimeout)
	return 0
}

// Lookup resolves a per-node directory by display name. The display name
// is translated back to the node ID via the labels map so that COSI calls
// always use the original ID regardless of what label is shown on disk.
func (n *nodeDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gfuse.Inode, syscall.Errno) {
	nodeID, ok := n.labels[name]
	if !ok {
		return nil, syscall.ENOENT
	}

	defs, err := n.opts.Repository.ResourceDefinitions(ctx, nodeID)
	if err != nil {
		return nil, grpcErrno(err)
	}

	stable := gfuse.StableAttr{Mode: fuse.S_IFDIR}
	child := n.NewInode(ctx, &perNodeDir{opts: n.opts, name: nodeID, defs: defs}, stable)
	out.Attr.Mode = dirMode
	out.SetAttrTimeout(dirTimeout)
	out.SetEntryTimeout(dirTimeout)

	return child, 0
}

// Readdir lists node display names as directories. The order follows the
// original Nodes slice so that the listing is stable across calls.
func (n *nodeDir) Readdir(_ context.Context) (gfuse.DirStream, syscall.Errno) {
	entries := make([]fuse.DirEntry, 0, len(n.nodes))
	for _, id := range n.nodes {
		displayName := id
		if nl := n.opts.NodeLabels; nl != nil {
			if label, ok := nl[id]; ok && label != "" {
				displayName = label
			}
		}
		entries = append(entries, fuse.DirEntry{Name: displayName, Mode: fuse.S_IFDIR})
	}
	return gfuse.NewListDirStream(entries), 0
}

// ----- perNodeDir (per-node directory under /nodes) -----

// perNodeDir is a per-node directory. Its children are the namespace
// directories for the node it represents.
type perNodeDir struct {
	gfuse.Inode
	opts Options
	name string // actual node name
	defs []*meta.ResourceDefinition
}

// Getattr implements fs.NodeGetattrer.
func (n *perNodeDir) Getattr(_ context.Context, _ gfuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = dirMode
	out.SetTimeout(dirTimeout)
	return 0
}

// Lookup resolves a namespace name under this specific node. The
// namespaceDir is constructed with the actual node name so that
// downstream repository calls are scoped correctly.
func (n *perNodeDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gfuse.Inode, syscall.Errno) {
	if !findNamespace(n.defs, name) {
		return nil, syscall.ENOENT
	}

	stable := gfuse.StableAttr{Mode: fuse.S_IFDIR}
	child := n.NewInode(ctx, &namespaceDir{opts: n.opts, namespace: name, defs: n.defs, node: n.name}, stable)
	out.Attr.Mode = dirMode
	out.SetAttrTimeout(dirTimeout)
	out.SetEntryTimeout(dirTimeout)

	return child, 0
}

// Readdir lists the namespace directories for this node and kicks off a
// background pre-warm of the ListResources cache for every resource type
// on this node. This means a subsequent recursive tree walk (e.g. eza -T)
// finds warm cache entries instead of blocking on sequential API calls.
func (n *perNodeDir) Readdir(_ context.Context) (gfuse.DirStream, syscall.Errno) {
	names := namespaceNames(n.defs)
	entries := make([]fuse.DirEntry, 0, len(names))
	for _, nm := range names {
		entries = append(entries, fuse.DirEntry{Name: nm, Mode: fuse.S_IFDIR})
	}
	go prefetchNodeLists(context.Background(), n.opts.Repository, n.name, n.defs)
	return gfuse.NewListDirStream(entries), 0
}

// prefetchNodeLists warms the ListResources cache for every resource type
// defined on node, running up to prefetchConcurrency calls in parallel.
// Errors are silently dropped; a cache miss on the subsequent real access
// is the correct fallback.
const prefetchConcurrency = 8

func prefetchNodeLists(ctx context.Context, repo ResourceRepository, node string, defs []*meta.ResourceDefinition) {
	sem := make(chan struct{}, prefetchConcurrency)
	var wg sync.WaitGroup
	for _, rd := range defs {
		ns := rd.TypedSpec().DefaultNamespace
		if ns == "" {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(namespace, resourceType string) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _ = repo.ListResources(ctx, node, namespace, resourceType)
		}(ns, string(rd.TypedSpec().Type))
	}
	wg.Wait()
}

// ----- namespaceDir -----

// namespaceDir exposes resource-type directories under a namespace.
type namespaceDir struct {
	gfuse.Inode
	opts      Options
	namespace string
	node      string // set when under a nodeDir; "" for single-node root
	defs      []*meta.ResourceDefinition
}

// Getattr implements fs.NodeGetattrer.
func (n *namespaceDir) Getattr(_ context.Context, _ gfuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = dirMode
	out.SetTimeout(dirTimeout)
	return 0
}

// children lists resource-type display names for this namespace.
func (n *namespaceDir) children() []string {
	seen := map[string]bool{}
	var out []string

	for _, rd := range n.defs {
		if rd.TypedSpec().DefaultNamespace != n.namespace {
			continue
		}
		name := displayNameFor(rd)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	sort.Strings(out)
	return out
}

// findResourceType resolves a display name back to its canonical
// ResourceDefinition, honouring alias matches.
func (n *namespaceDir) findResourceType(name string) *meta.ResourceDefinition {
	for _, rd := range n.defs {
		if rd.TypedSpec().DefaultNamespace != n.namespace {
			continue
		}
		if displayNameFor(rd) == name {
			return rd
		}
	}
	return nil
}

// Lookup resolves a resource-type directory name.
func (n *namespaceDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gfuse.Inode, syscall.Errno) {
	rd := n.findResourceType(name)
	if rd == nil {
		return nil, syscall.ENOENT
	}

	stable := gfuse.StableAttr{Mode: fuse.S_IFDIR}
	child := n.NewInode(ctx, &resourceTypeDir{
		opts:      n.opts,
		node:      n.node,
		namespace: n.namespace,
		name:      name,
		rd:        rd,
	}, stable)
	out.Attr.Mode = dirMode
	out.SetAttrTimeout(dirTimeout)
	out.SetEntryTimeout(dirTimeout)

	return child, 0
}

// Readdir lists resource-type directories.
func (n *namespaceDir) Readdir(_ context.Context) (gfuse.DirStream, syscall.Errno) {
	names := n.children()
	entries := make([]fuse.DirEntry, 0, len(names))
	for _, nm := range names {
		entries = append(entries, fuse.DirEntry{Name: nm, Mode: fuse.S_IFDIR})
	}
	return gfuse.NewListDirStream(entries), 0
}

// ----- resourceTypeDir -----

// resourceTypeDir exposes individual resource files for a single
// (namespace, type) pair.
type resourceTypeDir struct {
	gfuse.Inode
	opts      Options
	node      string // "" in single-node mode
	namespace string
	name      string // display name (alias or canonical)
	rd        *meta.ResourceDefinition
}

// Getattr implements fs.NodeGetattrer.
func (r *resourceTypeDir) Getattr(_ context.Context, _ gfuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = dirMode
	out.SetTimeout(dirTimeout)
	return 0
}

// Lookup resolves a resource id file under the type directory. The id
// portion is URL-un-escaped before being handed to the repository.
func (r *resourceTypeDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gfuse.Inode, syscall.Errno) {
	idRaw, ext, err := splitIDAndExtension(name)
	if err != nil {
		return nil, syscall.ENOENT
	}

	id := unescapeID(idRaw)

	rf := &resourceFile{
		opts:      r.opts,
		node:      r.node,
		namespace: r.namespace,
		rd:        r.rd,
		id:        id,
		ext:       ext,
	}

	stable := gfuse.StableAttr{Mode: fuse.S_IFREG}
	child := r.NewInode(ctx, rf, stable)

	out.Attr.Mode = fileMode(r.opts.Writeable && !r.opts.Maintenance)
	out.SetAttrTimeout(fileAttrTimeout)
	out.SetEntryTimeout(dirTimeout)

	return child, 0
}

// Readdir lists resource files within this type directory. Permission-denied
// errors are treated as empty directories so recursive tree walks skip them
// cleanly rather than reporting an error for every restricted type.
func (r *resourceTypeDir) Readdir(ctx context.Context) (gfuse.DirStream, syscall.Errno) {
	items, err := r.opts.Repository.ListResources(ctx, r.node, r.namespace, r.rd.TypedSpec().Type)
	if err != nil {
		if grpcErrno(err) == syscall.EACCES {
			return gfuse.NewListDirStream(nil), 0
		}
		return nil, grpcErrno(err)
	}

	ext := fileExtensionFor(r.opts.Format)
	entries := make([]fuse.DirEntry, 0, len(items))
	for _, item := range items {
		id := item.Metadata().ID()
		entries = append(entries, fuse.DirEntry{
			Name: escapeID(string(id)) + ext,
			Mode: fuse.S_IFREG,
		})
	}

	return gfuse.NewListDirStream(entries), 0
}

// ----- resourceFile -----

// resourceFile represents a single resource document on disk.
type resourceFile struct {
	gfuse.Inode
	opts      Options
	node      string
	namespace string
	rd        *meta.ResourceDefinition
	id        string
	ext       string

	contentMu  sync.Mutex
	content    []byte
	contentErr error
}

// fileMode returns the appropriate file mode for a resource file
// based on writability.
func fileMode(writeable bool) uint32 {
	if writeable {
		return fileModeRW
	}
	return fileModeRO
}

// Getattr implements fs.NodeGetattrer. Size is reported from the cached
// content if available; otherwise 0 is returned and the kernel re-stats
// after the first Read populates the cache. Triggering a fetch here
// would make every directory traversal (e.g. eza -T) issue a GetResource
// call per file, which is prohibitively expensive over a remote API.
func (f *resourceFile) Getattr(_ context.Context, _ gfuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fileMode(f.opts.Writeable && !f.opts.Maintenance)
	f.contentMu.Lock()
	out.Size = uint64(len(f.content))
	f.contentMu.Unlock()
	out.SetTimeout(fileAttrTimeout)
	return 0
}

// Setattr implements fs.NodeSetattrer. It only honours FATTR_SIZE so
// that opening a file with O_TRUNC succeeds on writable mounts. Other
// attribute changes are ignored; the file mode is controlled by the
// mount writability.
func (f *resourceFile) Setattr(_ context.Context, _ gfuse.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if f.opts.Maintenance {
		return syscall.EROFS
	}
	if !f.opts.Writeable {
		return syscall.EACCES
	}

	if in.Valid&fuse.FATTR_SIZE != 0 {
		// A truncate invalidates any cached read content. The actual
		// buffer resize happens in the per-open fileHandle.
		f.invalidateContent()
	}

	out.Mode = fileMode(f.opts.Writeable && !f.opts.Maintenance)
	out.Size = in.Size
	out.SetTimeout(fileAttrTimeout)

	return 0
}

// Open implements fs.NodeOpener. FOPEN_DIRECT_IO tells the kernel to
// bypass its page cache and always forward reads to FUSE. This is
// required because Lookup returns size=0 (content is unknown until
// fetched), and without DIRECT_IO the kernel would short-circuit all
// reads against that cached zero size.
func (f *resourceFile) Open(_ context.Context, _ uint32) (gfuse.FileHandle, uint32, syscall.Errno) {
	return &fileHandle{file: f}, fuse.FOPEN_DIRECT_IO, 0
}

// Read implements fs.NodeReader. It fetches the resource on first access
// and caches the result; subsequent reads within the same inode lifetime
// are served from memory.
func (f *resourceFile) Read(ctx context.Context, _ gfuse.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, err := f.fetch(ctx)
	if err != nil {
		return nil, grpcErrno(err)
	}

	if off < 0 {
		off = 0
	}
	if off >= int64(len(data)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(data)) {
		end = int64(len(data))
	}

	return fuse.ReadResultData(data[off:end]), 0
}

// Write implements fs.NodeWriter. Buffering is handled by the per-open
// fileHandle returned from Open; this just forwards to it.
func (f *resourceFile) Write(ctx context.Context, h gfuse.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if fh, ok := h.(*fileHandle); ok {
		return fh.Write(ctx, data, off)
	}
	return 0, syscall.EIO
}

// fetch retrieves (and caches) the formatted file content.
func (f *resourceFile) fetch(ctx context.Context) ([]byte, error) {
	f.contentMu.Lock()
	defer f.contentMu.Unlock()

	if f.content != nil || f.contentErr != nil {
		return f.content, f.contentErr
	}

	rsrc, err := f.opts.Repository.GetResource(ctx, f.node, f.namespace, f.rd.TypedSpec().Type, f.id)
	if err != nil {
		f.contentErr = err
		return nil, err
	}

	data, err := resourceutil.FormatResource(rsrc, normalizeFormat(f.opts.Format))
	if err != nil {
		f.contentErr = err
		return nil, err
	}

	f.content = data
	return f.content, nil
}

// ----- helpers -----

// displayNameFor returns the directory name to use for the given
// ResourceDefinition. The first alias wins; otherwise the canonical
// type name is used. Aliases are kept lower-cased.
func displayNameFor(rd *meta.ResourceDefinition) string {
	spec := rd.TypedSpec()
	if len(spec.Aliases) > 0 {
		return strings.ToLower(string(spec.Aliases[0]))
	}
	return strings.ToLower(string(spec.Type))
}

// namespaceNames returns the sorted set of namespaces present in the
// given ResourceDefinitions.
func namespaceNames(defs []*meta.ResourceDefinition) []string {
	seen := map[string]bool{}
	for _, rd := range defs {
		ns := rd.TypedSpec().DefaultNamespace
		if ns == "" {
			continue
		}
		seen[ns] = true
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findNamespace reports whether any ResourceDefinition declares the
// given namespace as its default.
func findNamespace(defs []*meta.ResourceDefinition, ns string) bool {
	for _, rd := range defs {
		if rd.TypedSpec().DefaultNamespace == ns {
			return true
		}
	}
	return false
}

// compile-time interface checks for the FUSE node types.
var (
	_ gfuse.NodeGetattrer = (*TalosRoot)(nil)
	_ gfuse.NodeLookuper  = (*TalosRoot)(nil)
	_ gfuse.NodeReaddirer = (*TalosRoot)(nil)

	_ gfuse.NodeGetattrer = (*nodeDir)(nil)
	_ gfuse.NodeLookuper  = (*nodeDir)(nil)
	_ gfuse.NodeReaddirer = (*nodeDir)(nil)

	_ gfuse.NodeGetattrer = (*perNodeDir)(nil)
	_ gfuse.NodeLookuper  = (*perNodeDir)(nil)
	_ gfuse.NodeReaddirer = (*perNodeDir)(nil)

	_ gfuse.NodeGetattrer = (*namespaceDir)(nil)
	_ gfuse.NodeLookuper  = (*namespaceDir)(nil)
	_ gfuse.NodeReaddirer = (*namespaceDir)(nil)

	_ gfuse.NodeGetattrer = (*resourceTypeDir)(nil)
	_ gfuse.NodeLookuper  = (*resourceTypeDir)(nil)
	_ gfuse.NodeReaddirer = (*resourceTypeDir)(nil)

	_ gfuse.NodeGetattrer = (*resourceFile)(nil)
	_ gfuse.NodeSetattrer = (*resourceFile)(nil)
	_ gfuse.NodeOpener    = (*resourceFile)(nil)
	_ gfuse.NodeReader    = (*resourceFile)(nil)
	_ gfuse.NodeWriter    = (*resourceFile)(nil)
)

// grpcErrno translates a (possibly wrapped) gRPC status error into the
// most appropriate syscall.Errno for reporting through FUSE. Walking the
// error chain is necessary because the repository layers wrap errors with
// fmt.Errorf before returning them.
func grpcErrno(err error) syscall.Errno {
	for e := err; e != nil; e = errors.Unwrap(e) {
		s, ok := status.FromError(e)
		if !ok {
			continue
		}
		switch s.Code() {
		case codes.PermissionDenied, codes.Unauthenticated:
			return syscall.EACCES
		case codes.NotFound:
			return syscall.ENOENT
		case codes.Unimplemented:
			return syscall.ENOSYS
		default:
			return syscall.EIO
		}
	}
	return syscall.EIO
}

// ensure resource package import remains in scope even if individual
// helpers are reorganised later.
var _ resource.Resource = (resource.Resource)(nil)
