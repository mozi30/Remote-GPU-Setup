// gpu-backpack: rent a cheap GPU, keep the workspace in Cloudflare R2.
package main

import (
	"fmt"
	"os"

	"github.com/mozi30/Remote-GPU-Setup/internal/app"
)

var version = "dev" // set by -ldflags "-X main.version=..."

func main() {
	if err := app.Run(version); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Print("press Enter to close...")
		fmt.Scanln()
		os.Exit(1)
	}
}
