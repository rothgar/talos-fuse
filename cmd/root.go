// Package cmd implements the talos-fuse CLI commands.
package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/spf13/cobra"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"go.yaml.in/yaml/v4"

	"github.com/jgarr/talos-fuse/internal/fs"
	"github.com/jgarr/talos-fuse/internal/resourceutil"
	"github.com/jgarr/talos-fuse/internal/talos"
)

// RunE is the cobra RunE function for the root command. It wires the
// CLI flags into a talos.Repository, builds the FUSE adapter and
// blocks in fs.Mount until the filesystem is unmounted.
func RunE(cmd *cobra.Command, _ []string) error {
	mountpoint, err := cmd.Flags().GetString("mount")
	if err != nil {
		return err
	}

	nodes, err := cmd.Flags().GetStringSlice("nodes")
	if err != nil {
		return err
	}

	endpoints, err := cmd.Flags().GetStringSlice("endpoints")
	if err != nil {
		return err
	}

	talosconfig, err := cmd.Flags().GetString("talosconfig")
	if err != nil {
		return err
	}

	contextName, err := cmd.Flags().GetString("context")
	if err != nil {
		return err
	}

	cluster, err := cmd.Flags().GetString("cluster")
	if err != nil {
		return err
	}

	maintenance, err := cmd.Flags().GetBool("maintenance")
	if err != nil {
		return err
	}

	syncFlag, err := cmd.Flags().GetBool("sync")
	if err != nil {
		return err
	}

	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}

	format = strings.ToLower(format)
	switch format {
	case "yaml", "json":
		// valid
	default:
		return fmt.Errorf("invalid --format %q: must be \"yaml\" or \"json\"", format)
	}

	debug, err := cmd.Flags().GetBool("debug")
	if err != nil {
		return err
	}

	cacheTTL, err := cmd.Flags().GetDuration("cache-ttl")
	if err != nil {
		return err
	}

	// Block --sync on Omni-managed clusters. Omni reconciles machine
	// config through its own patch system; direct COSI writes bypass that
	// and will be silently reverted on the next reconcile cycle.
	if syncFlag && !maintenance {
		if omni, err := isOmniContext(talosconfig, contextName); err == nil && omni {
			return fmt.Errorf("--sync is not supported for Omni-managed clusters: writes bypass Omni's patch reconciliation and will be reverted; use omnictl or the Omni UI to apply configuration changes")
		}
	}

	repo := talos.NewRepository(maintenance, nodes, endpoints)

	effectiveNodes, err := repo.ResolveNodes(talosconfig, contextName)
	if err != nil {
		return err
	}

	adapter := &fs.RepositoryAdapter{
		Repo:        repo,
		Talosconfig: talosconfig,
		ContextName: contextName,
		Cluster:     cluster,
	}

	// Resolve each node UUID to its hostname so directory listings show
	// meaningful names. Failures are non-fatal: the UUID is used instead.
	nodeLabels := resolveNodeHostnames(context.Background(), adapter, effectiveNodes)

	// Wrap the adapter with a TTL cache and pre-warm ResourceDefinitions
	// for all nodes so the first directory listing is served from memory.
	cached := fs.NewCachingRepository(adapter, cacheTTL)
	if !maintenance {
		cached.Prefetch(context.Background(), effectiveNodes)
	}

	fuseOpts := &fs.Options{
		Repository:  cached,
		Maintenance: maintenance,
		Writeable:   syncFlag && !maintenance,
		Format:      format,
		Nodes:       effectiveNodes,
		Cluster:     cluster,
		NodeLabels:  nodeLabels,
		MountOptions: fuse.MountOptions{
			Name:  "talos-fuse",
			Debug: debug,
		},
	}

	server, err := fs.Mount(mountpoint, &fs.TalosRoot{}, fuseOpts)
	if err != nil {
		return err
	}

	server.Wait()
	return nil
}

// isOmniContext reports whether the selected talosconfig context is
// managed by Omni. Omni contexts carry a non-empty cluster field that
// the Omni proxy uses for routing; direct Talos contexts do not set it.
// Errors opening the talosconfig are returned so the caller can decide
// whether to treat them as fatal.
func isOmniContext(talosconfig, contextName string) (bool, error) {
	cfg, err := clientconfig.Open(talosconfig)
	if err != nil {
		return false, err
	}
	name := contextName
	if name == "" {
		name = cfg.Context
	}
	ctx := cfg.Contexts[name]
	if ctx == nil {
		return false, nil
	}
	return ctx.Cluster != "", nil
}

// resolveNodeHostnames fetches HostnameStatuses/hostname from each node
// in parallel and returns a map of nodeID → hostname. Nodes that cannot
// be reached or whose hostname cannot be parsed are omitted (the UUID
// is used instead).
func resolveNodeHostnames(ctx context.Context, adapter *fs.RepositoryAdapter, nodes []string) map[string]string {
	var mu sync.Mutex
	labels := make(map[string]string, len(nodes))

	var wg sync.WaitGroup
	for _, nodeID := range nodes {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			rsrc, err := adapter.GetResource(ctx, id, "network", "HostnameStatuses.net.talos.dev", "hostname")
			if err != nil {
				return
			}
			yamlBytes, err := resourceutil.FormatResource(rsrc, "yaml")
			if err != nil {
				return
			}
			var doc struct {
				Spec struct {
					Hostname string `yaml:"hostname"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
				return
			}
			if doc.Spec.Hostname != "" {
				mu.Lock()
				labels[id] = doc.Spec.Hostname
				mu.Unlock()
			}
		}(nodeID)
	}
	wg.Wait()
	return labels
}

var rootCmd = &cobra.Command{
	Use:   "talos-fuse",
	Short: "Expose Talos API resources as a FUSE filesystem",
	Long: `talos-fuse mounts a FUSE filesystem that mirrors Talos API resources.

Resources are addressed by (namespace, type, id) and rendered as YAML by
default. Use --format json for JSON output and --sync to push modifications
back via the COSI resource API.`,
	RunE: RunE,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.Flags().StringP("mount", "m", "./", "mount point path")
	rootCmd.Flags().StringSliceP("nodes", "n", nil, "target nodes (comma-separated)")
	rootCmd.Flags().StringSliceP("endpoints", "e", nil, "override endpoints (comma-separated)")
	rootCmd.Flags().String("talosconfig", "", "talosconfig path")
	rootCmd.Flags().String("context", "", "talosconfig context")
	rootCmd.Flags().String("cluster", "", "cluster name")
	rootCmd.Flags().BoolP("maintenance", "i", false, "connect in maintenance mode (insecure TLS)")
	rootCmd.Flags().BoolP("sync", "s", false, "enable two-way sync (resource writes)")
	rootCmd.Flags().String("format", "yaml", "file format: yaml or json")
	rootCmd.Flags().Bool("debug", false, "enable FUSE debug logging")
	rootCmd.Flags().Duration("cache-ttl", 5*time.Minute, "TTL for cached resource definitions and listings (0 to disable)")
}
