// Command serve is the OUT-OF-PROCESS entrypoint for the firecracker substrate plugin: dual-mode
// sdk.Main (serve OR CLI). charly fork/execs this binary in CLI mode for `charly firecracker …`
// (→ CliMain); the serve half answers the gRPC Describe/Invoke channel. The SAME provider compiles
// INTO charly in-process, so placement is invisible to the check verb either way.
package main

import (
	firecracker "github.com/opencharly/plugin-firecracker/candy/plugin-firecracker"
	"github.com/opencharly/sdk"
)

func main() {
	sdk.Main(firecracker.NewProvider(), firecracker.NewMeta(), firecracker.CliMain)
}
