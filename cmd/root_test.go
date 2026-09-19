package cmd

import (
	"testing"
)

// resetFlags clears all flag values between tests so defaults can be asserted
// and prior flag state does not leak between runs.
func resetFlags(t *testing.T) {
	t.Helper()
	flags := []string{"mount", "nodes", "endpoints", "talosconfig", "context", "cluster", "maintenance", "sync", "format", "debug"}
	for _, name := range flags {
		if err := rootCmd.Flags().Set(name, ""); err != nil {
			// StringSlice flags reject ""; clear via the typed accessor instead.
			continue
		}
	}
	// Reset string slice flags by reassigning to nil.
	if err := rootCmd.Flags().Set("nodes", ""); err != nil {
		_ = rootCmd.Flags().Set("nodes", "a")
		_ = rootCmd.Flags().Set("nodes", "")
	}
	if err := rootCmd.Flags().Set("endpoints", ""); err != nil {
		_ = rootCmd.Flags().Set("endpoints", "a")
		_ = rootCmd.Flags().Set("endpoints", "")
	}
}

func TestFlagDefaults(t *testing.T) {
	resetFlags(t)

	cases := []struct {
		flag     string
		wantStr  string
		wantBool bool
		isBool   bool
		isString bool
	}{
		{flag: "mount", wantStr: "./", isString: true},
		{flag: "format", wantStr: "yaml", isString: true},
		{flag: "maintenance", wantBool: false, isBool: true},
		{flag: "sync", wantBool: false, isBool: true},
		{flag: "debug", wantBool: false, isBool: true},
	}

	for _, tc := range cases {
		f := rootCmd.Flags().Lookup(tc.flag)
		if f == nil {
			t.Fatalf("flag %q not registered", tc.flag)
		}
		switch {
		case tc.isBool:
			if f.Value.String() != "false" {
				t.Errorf("flag %q default = %q, want %q", tc.flag, f.Value.String(), "false")
			}
		case tc.isString:
			if f.DefValue != tc.wantStr {
				t.Errorf("flag %q default = %q, want %q", tc.flag, f.DefValue, tc.wantStr)
			}
		}
	}
}

func TestFlagShorthands(t *testing.T) {
	cases := []struct {
		flag      string
		shorthand string
	}{
		{"mount", "m"},
		{"nodes", "n"},
		{"endpoints", "e"},
		{"maintenance", "i"},
		{"sync", "s"},
	}

	for _, tc := range cases {
		f := rootCmd.Flags().Lookup(tc.flag)
		if f == nil {
			t.Fatalf("flag %q not registered", tc.flag)
		}
		if f.Shorthand != tc.shorthand {
			t.Errorf("flag %q shorthand = %q, want %q", tc.flag, f.Shorthand, tc.shorthand)
		}
	}
}

func TestBoolFlagParse(t *testing.T) {
	resetFlags(t)

	for _, name := range []string{"maintenance", "sync", "debug"} {
		if err := rootCmd.Flags().Set(name, "true"); err != nil {
			t.Fatalf("set %s=true: %v", name, err)
		}
		v, err := rootCmd.Flags().GetBool(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if !v {
			t.Errorf("flag %q = false after Set(true), want true", name)
		}
		if err := rootCmd.Flags().Set(name, "false"); err != nil {
			t.Fatalf("set %s=false: %v", name, err)
		}
		v, err = rootCmd.Flags().GetBool(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if v {
			t.Errorf("flag %q = true after Set(false), want false", name)
		}
	}
}

func TestStringFlagsParse(t *testing.T) {
	resetFlags(t)

	if err := rootCmd.Flags().Set("mount", "/mnt/talos"); err != nil {
		t.Fatalf("set mount: %v", err)
	}
	v, err := rootCmd.Flags().GetString("mount")
	if err != nil {
		t.Fatalf("get mount: %v", err)
	}
	if v != "/mnt/talos" {
		t.Errorf("mount = %q, want %q", v, "/mnt/talos")
	}

	if err := rootCmd.Flags().Set("format", "json"); err != nil {
		t.Fatalf("set format: %v", err)
	}
	v, err = rootCmd.Flags().GetString("format")
	if err != nil {
		t.Fatalf("get format: %v", err)
	}
	if v != "json" {
		t.Errorf("format = %q, want %q", v, "json")
	}
}

func TestStringSliceFlagsParse(t *testing.T) {
	resetFlags(t)

	if err := rootCmd.Flags().Set("nodes", "10.0.0.2,10.0.0.3"); err != nil {
		t.Fatalf("set nodes: %v", err)
	}
	v, err := rootCmd.Flags().GetStringSlice("nodes")
	if err != nil {
		t.Fatalf("get nodes: %v", err)
	}
	if len(v) != 2 || v[0] != "10.0.0.2" || v[1] != "10.0.0.3" {
		t.Errorf("nodes = %v, want [10.0.0.2 10.0.0.3]", v)
	}

	if err := rootCmd.Flags().Set("endpoints", "https://10.0.0.2:50000"); err != nil {
		t.Fatalf("set endpoints: %v", err)
	}
	v, err = rootCmd.Flags().GetStringSlice("endpoints")
	if err != nil {
		t.Fatalf("get endpoints: %v", err)
	}
	if len(v) != 1 || v[0] != "https://10.0.0.2:50000" {
		t.Errorf("endpoints = %v, want [https://10.0.0.2:50000]", v)
	}
}

func TestRootCommandMetadata(t *testing.T) {
	if rootCmd.Use != "talos-fuse" {
		t.Errorf("rootCmd.Use = %q, want %q", rootCmd.Use, "talos-fuse")
	}
	if rootCmd.Short == "" {
		t.Error("rootCmd.Short is empty")
	}
}
