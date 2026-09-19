package talos

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestNewRepositoryAccessors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		maintenance bool
		nodes       []string
		endpoints   []string
	}{
		{
			name:        "normal mode with nodes and endpoints",
			maintenance: false,
			nodes:       []string{"10.0.0.1", "10.0.0.2"},
			endpoints:   []string{"10.0.0.1:50000"},
		},
		{
			name:        "maintenance mode",
			maintenance: true,
			nodes:       []string{"10.0.0.5"},
			endpoints:   nil,
		},
		{
			name:        "empty configuration",
			maintenance: false,
			nodes:       nil,
			endpoints:   nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := NewRepository(tt.maintenance, tt.nodes, tt.endpoints)
			if r == nil {
				t.Fatal("NewRepository returned nil")
			}
			if got := r.Maintenance(); got != tt.maintenance {
				t.Errorf("Maintenance() = %v, want %v", got, tt.maintenance)
			}
			if !reflect.DeepEqual(r.Endpoints(), tt.endpoints) {
				t.Errorf("Endpoints() = %v, want %v", r.Endpoints(), tt.endpoints)
			}
		})
	}
}

func TestResolveNodesReturnsConfiguredNodes(t *testing.T) {
	t.Parallel()

	r := NewRepository(false, []string{"10.0.0.9"}, nil)
	got, err := r.ResolveNodes("", "")
	if err != nil {
		t.Fatalf("ResolveNodes: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"10.0.0.9"}) {
		t.Errorf("ResolveNodes = %v, want [10.0.0.9]", got)
	}
}

func TestResolveNodesFromTalosconfig(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join("testdata", "talosconfig")
	r := NewRepository(false, nil, nil)
	got, err := r.ResolveNodes(cfgPath, "test")
	if err != nil {
		t.Fatalf("ResolveNodes: %v", err)
	}
	want := []string{"10.0.0.1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveNodes = %v, want %v", got, want)
	}
}

func TestResolveNodesMissingContext(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join("testdata", "talosconfig")
	r := NewRepository(false, nil, nil)
	if _, err := r.ResolveNodes(cfgPath, "does-not-exist"); err == nil {
		t.Fatal("expected error for missing context, got nil")
	}
}
