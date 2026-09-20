package talos

import (
	"context"
	"fmt"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/talos/pkg/machinery/client"
)

// ResourceDefinitions returns every ResourceDefinition advertised by the
// connected node(s). It is the basis for enumerating namespaces and
// resource types when building the FUSE tree.
func (r *Repository) ResourceDefinitions(ctx context.Context, c *client.Client) ([]*meta.ResourceDefinition, error) {
	defs, err := safe.StateListAll[*meta.ResourceDefinition](ctx, c.COSI)
	if err != nil {
		return nil, fmt.Errorf("list resource definitions: %w", err)
	}

	out := make([]*meta.ResourceDefinition, 0, defs.Len())
	for rd := range defs.All() {
		out = append(out, rd)
	}
	return out, nil
}

// ResolveResourceKind maps a user-supplied kind (canonical name or alias)
// to its ResourceDefinition. If namespace is empty, the underlying API
// fills it in with the resource's default namespace.
func (r *Repository) ResolveResourceKind(
	ctx context.Context,
	c *client.Client,
	namespace, kind string,
) (*meta.ResourceDefinition, error) {
	ns := resource.Namespace(namespace)
	rd, err := c.ResolveResourceKind(ctx, &ns, resource.Type(kind))
	if err != nil {
		return nil, fmt.Errorf("resolve resource kind %q: %w", kind, err)
	}
	return rd, nil
}

// ListResources lists every resource of rd's type under namespace. The
// caller is responsible for scoping ctx to the target node via
// client.WithNode before calling. The protobuf unmarshaler is skipped
// so the returned resources carry their typed specs without needing
// client-side registration of every Talos resource type.
func (r *Repository) ListResources(
	ctx context.Context,
	c *client.Client,
	namespace string,
	rd *meta.ResourceDefinition,
) ([]resource.Resource, error) {
	if rd == nil {
		return nil, fmt.Errorf("resource definition is nil")
	}

	list, err := c.COSI.List(
		ctx,
		resource.NewMetadata(resource.Namespace(namespace), rd.TypedSpec().Type, "", resource.VersionUndefined),
		state.WithListUnmarshalOptions(state.WithSkipProtobufUnmarshal()),
	)
	if err != nil {
		return nil, fmt.Errorf("list resources %s/%s: %w", namespace, rd.TypedSpec().Type, err)
	}

	items := make([]resource.Resource, 0, len(list.Items))
	items = append(items, list.Items...)
	return items, nil
}

// GetResource fetches a single resource by namespace and id. The caller
// is responsible for scoping ctx to the target node before calling.
// The protobuf unmarshaler is skipped for the same reason as ListResources.
func (r *Repository) GetResource(
	ctx context.Context,
	c *client.Client,
	namespace, id string,
	rd *meta.ResourceDefinition,
) (resource.Resource, error) {
	if rd == nil {
		return nil, fmt.Errorf("resource definition is nil")
	}

	rsrc, err := c.COSI.Get(
		ctx,
		resource.NewMetadata(resource.Namespace(namespace), rd.TypedSpec().Type, resource.ID(id), resource.VersionUndefined),
		state.WithGetUnmarshalOptions(state.WithSkipProtobufUnmarshal()),
	)
	if err != nil {
		return nil, fmt.Errorf("get resource %s/%s/%s: %w", namespace, rd.TypedSpec().Type, id, err)
	}
	return rsrc, nil
}

// UpdateResource writes a resource back to the API. The caller is
// responsible for scoping ctx to the target node before calling.
func (r *Repository) UpdateResource(
	ctx context.Context,
	c *client.Client,
	rsrc resource.Resource,
) error {
	if rsrc == nil {
		return fmt.Errorf("resource is nil")
	}

	if err := c.COSI.Update(ctx, rsrc); err != nil {
		return fmt.Errorf("update resource %s/%s/%s: %w",
			rsrc.Metadata().Namespace(),
			rsrc.Metadata().Type(),
			rsrc.Metadata().ID(),
			err,
		)
	}
	return nil
}
