package main

import (
	"os"
	"strings"

	"github.com/containers/oci-delta/cmd"
	"github.com/containers/storage/pkg/reexec"
	"github.com/containers/storage/pkg/unshare"
)

func main() {
	if reexec.Init() {
		return
	}
	// Only enter a user namespace when the invocation can touch container
	// storage. Pure OCI layout and OCI archive operations do not need it and
	// fail in environments that do not provide subordinate UID/GID ranges.
	if requiresUserNamespace(os.Args[1:]) {
		unshare.MaybeReexecUsingUserNamespace(false)
	}

	cmd.Execute()
}

func requiresUserNamespace(args []string) bool {
	if len(args) == 0 {
		return false
	}

	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			return false
		}
	}

	if args[0] == "import" {
		return true
	}

	for _, arg := range args {
		if arg == "--container-storage" ||
			strings.HasPrefix(arg, "--container-storage=") ||
			strings.HasPrefix(arg, "containers-storage:") {

			return true
		}
	}

	return false
}
