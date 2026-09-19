// Package talos wraps the Talos machinery client and COSI resource API
// behind a small Repository abstraction.
package talos

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
)

// Repository is the Talos resource backend.
//
// A Repository carries the static configuration (maintenance mode,
// target nodes, endpoint overrides) used to build per-call clients.
// Each call site creates a fresh *client.Client via WithClient so that
// callers do not have to manage client lifecycles.
type Repository struct {
	maintenance bool
	nodes       []string
	endpoints   []string
}

// NewRepository constructs a Repository.
//
// maintenance enables insecure-TLS maintenance mode (no talosconfig
// required). nodes are the target nodes (used both as gRPC endpoints in
// maintenance mode and as the set of nodes injected via WithNodes in
// normal mode). endpoints, if non-empty, override the endpoints derived
// from the loaded talosconfig context.
func NewRepository(maintenance bool, nodes, endpoints []string) *Repository {
	return &Repository{
		maintenance: maintenance,
		nodes:       nodes,
		endpoints:   endpoints,
	}
}

// Maintenance reports whether the repository is configured for
// maintenance mode.
func (r *Repository) Maintenance() bool { return r.maintenance }

// Endpoints returns the endpoint overrides, if any were configured.
func (r *Repository) Endpoints() []string { return r.endpoints }

// WithClient builds a fresh Talos client and invokes action.
//
// In maintenance mode no talosconfig is consulted: a TLS config with
// InsecureSkipVerify is built (matching `talosctl --insecure`), the
// provided endpoints come from nodes (or the explicit endpoints
// override), and WithEndpoints wires them up. client.WithNodes is
// applied so a single call fans out across every configured node.
//
// In normal mode talosconfig is opened via clientconfig.Open and the
// selected context feeds client.WithConfig. contextName, cluster, and
// endpoints are applied via the matching With* options. client.WithNodes
// is NOT applied here: against the Omni Sidero proxy the single
// configured endpoint refuses to fan out to multiple nodes
// ("one-2-many proxying is not supported" for /cosi.resource.State/List
// and similar methods). Per-node scoping is performed at the call site
// via client.WithNode.
//
// The client is always closed before WithClient returns, even when
// action returns an error.
func (r *Repository) WithClient(
	ctx context.Context,
	talosconfig, contextName, cluster string,
	action func(context.Context, *client.Client) error,
) error {
	c, err := r.newClient(ctx, talosconfig, contextName, cluster)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if r.maintenance {
		nodes, err := r.effectiveNodes(talosconfig, contextName)
		if err != nil {
			return err
		}
		if len(nodes) == 0 {
			return fmt.Errorf("no nodes configured: pass --nodes or set nodes in talosconfig context %q", contextNameOrDefault(contextName))
		}
		ctx = client.WithNodes(ctx, nodes...)
	}

	return action(ctx, c)
}

// effectiveNodes returns the nodes that should be targeted for this
// call. Explicit --nodes win; otherwise the selected context's Nodes
// field is consulted.
func (r *Repository) effectiveNodes(talosconfig, contextName string) ([]string, error) {
	if len(r.nodes) > 0 {
		return r.nodes, nil
	}
	cfg, err := clientconfig.Open(talosconfig)
	if err != nil {
		return nil, fmt.Errorf("open talosconfig: %w", err)
	}
	return resolveNodesFromConfig(cfg, contextName)
}

// newClient constructs a *client.Client using the repository's settings.
// It is the single place where normal-mode and maintenance-mode wiring
// diverge.
func (r *Repository) newClient(
	ctx context.Context,
	talosconfig, contextName, cluster string,
) (*client.Client, error) {
	if r.maintenance {
		opts := []client.OptionFunc{
			client.WithDefaultGRPCDialOptions(),
			client.WithTLSConfig(maintenanceTLSConfig()),
		}
		endpoints := r.endpoints
		if len(endpoints) == 0 {
			endpoints = r.nodes
		}
		if len(endpoints) > 0 {
			opts = append(opts, client.WithEndpoints(endpoints...))
		}
		return client.New(ctx, opts...)
	}

	cfg, err := clientconfig.Open(talosconfig)
	if err != nil {
		return nil, fmt.Errorf("open talosconfig: %w", err)
	}

	opts := []client.OptionFunc{
		client.WithConfig(cfg),
		client.WithDefaultGRPCDialOptions(),
		client.WithSideroV1KeysDir(clientconfig.CustomSideroV1KeysDirPath("")),
	}
	if contextName != "" {
		opts = append(opts, client.WithContextName(contextName))
	}
	if cluster != "" {
		opts = append(opts, client.WithCluster(cluster))
	}
	if len(r.endpoints) > 0 {
		opts = append(opts, client.WithEndpoints(r.endpoints...))
	}

	c, err := client.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("build talos client: %w", err)
	}
	return c, nil
}

// ResolveNodes returns the effective node set to target.
//
// If the Repository was constructed with --nodes, those nodes are
// returned unchanged. Otherwise the talosconfig is loaded and the
// selected context's Nodes field is returned. If the result is still
// empty, an error is returned advising the caller to pass --nodes.
func (r *Repository) ResolveNodes(talosconfig, contextName string) ([]string, error) {
	if len(r.nodes) > 0 {
		out := make([]string, len(r.nodes))
		copy(out, r.nodes)
		return out, nil
	}

	cfg, err := clientconfig.Open(talosconfig)
	if err != nil {
		return nil, fmt.Errorf("open talosconfig: %w", err)
	}

	nodes, err := resolveNodesFromConfig(cfg, contextName)
	if err != nil {
		return nil, err
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes configured: pass --nodes or set nodes in talosconfig context %q", contextNameOrDefault(contextName))
	}

	return nodes, nil
}

// resolveNodesFromConfig returns the nodes declared on the effective
// context of cfg. contextName, when non-empty, overrides the context
// named by cfg.Context. The returned slice is a copy; callers may mutate
// it freely.
func resolveNodesFromConfig(cfg *clientconfig.Config, contextName string) ([]string, error) {
	name := contextName
	if name == "" {
		name = cfg.Context
	}

	ctx := cfg.Contexts[name]
	if ctx == nil {
		return nil, fmt.Errorf("select context %q: context not found", contextNameOrDefault(name))
	}

	out := make([]string, len(ctx.Nodes))
	copy(out, ctx.Nodes)
	return out, nil
}

func contextNameOrDefault(name string) string {
	if name == "" {
		return "<default>"
	}
	return name
}

// maintenanceTLSConfig builds the TLS config used for maintenance
// connections. It matches the behavior of `talosctl --insecure`.
func maintenanceTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // intentional: maintenance mode
	}
}
