// talos-fuse exposes Talos API resources as a read-only-by-default FUSE
// filesystem. This is the entry point.
package main

import "github.com/jgarr/talos-fuse/cmd"

func main() {
	cmd.Execute()
}
