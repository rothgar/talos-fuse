// Package fs - ResourceRepository adapter for the Talos Repository.
//
// RepositoryAdapter bridges the FUSE layer's narrow ResourceRepository
// interface with the talos.Repository's higher-level methods (which take
// *client.Client, ResourceDefinition, etc.). It resolves the namespace
// and type into a ResourceDefinition once per call via the underlying
// Repository and applies client.WithNode when the FUSE path encodes a
// per-node prefix.
package fs

import (
	"context"
	"fmt"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/siderolabs/talos/pkg/machinery/client"

	"github.com/jgarr/talos-fuse/internal/talos"
)

// RepositoryAdapter implements ResourceRepository on top of
// *talos.Repository. The Talosconfig, ContextName and Cluster fields
// are passed through to every underlying call so that the configured
// context is selected on every gRPC connection.
type RepositoryAdapter struct {
	Repo        *talos.Repository
	Talosconfig string
	ContextName string
	Cluster     string
}

// ResourceDefinitions implements ResourceRepository by delegating to
// the underlying Talos Repository. The returned slice is safe to
// iterate concurrently.
func (a *RepositoryAdapter) ResourceDefinitions(ctx context.Context) ([]*meta.ResourceDefinition, error) {
	var defs []*meta.ResourceDefinition

	err := a.Repo.WithClient(ctx, a.Talosconfig, a.ContextName, a.Cluster,
		func(ctx context.Context, c *client.Client) error {
			d, err := a.Repo.ResourceDefinitions(ctx, c)
			if err != nil {
				return err
			}
			defs = d
			return nil
		})
	if err != nil {
		return nil, err
	}

	return defs, nil
}

// ListResources implements ResourceRepository. It resolves the
// resourceType into a ResourceDefinition via the connected node(s) and
// scopes the call to node when node is non-empty.
func (a *RepositoryAdapter) ListResources(
	ctx context.Context,
	node, namespace, resourceType string,
) ([]resource.Resource, error) {
	var items []resource.Resource

	err := a.Repo.WithClient(ctx, a.Talosconfig, a.ContextName, a.Cluster,
		func(ctx context.Context, c *client.Client) error {
			if node != "" {
				ctx = client.WithNode(ctx, node)
			}

			rd, err := a.Repo.ResolveResourceKind(ctx, c, namespace, resourceType)
			if err != nil {
				return err
			}

			list, err := a.Repo.ListResources(ctx, c, node, namespace, rd)
			if err != nil {
				return err
			}
			items = list
			return nil
		})
	if err != nil {
		return nil, err
	}

	return items, nil
}

// GetResource implements ResourceRepository. It resolves resourceType
// into a ResourceDefinition and fetches the resource identified by
// (namespace, id). The call is scoped to node when node is non-empty.
func (a *RepositoryAdapter) GetResource(
	ctx context.Context,
	node, namespace, resourceType, id string,
) (resource.Resource, error) {
	var rsrc resource.Resource

	err := a.Repo.WithClient(ctx, a.Talosconfig, a.ContextName, a.Cluster,
		func(ctx context.Context, c *client.Client) error {
			if node != "" {
				ctx = client.WithNode(ctx, node)
			}

			rd, err := a.Repo.ResolveResourceKind(ctx, c, namespace, resourceType)
			if err != nil {
				return err
			}

			r, err := a.Repo.GetResource(ctx, c, node, namespace, id, rd)
			if err != nil {
				return err
			}
			rsrc = r
			return nil
		})
	if err != nil {
		return nil, err
	}

	return rsrc, nil
}

// UpdateResource implements ResourceRepository. The call is scoped to
// node via client.WithNode when node is non-empty.
func (a *RepositoryAdapter) UpdateResource(
	ctx context.Context,
	node string,
	rsrc resource.Resource,
) error {
	if rsrc == nil {
		return fmt.Errorf("resource is nil")
	}

	return a.Repo.WithClient(ctx, a.Talosconfig, a.ContextName, a.Cluster,
		func(ctx context.Context, c *client.Client) error {
			if node != "" {
				ctx = client.WithNode(ctx, node)
			}
			return a.Repo.UpdateResource(ctx, c, node, rsrc)
		})
}
