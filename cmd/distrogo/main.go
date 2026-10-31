// Command distrogo instala distribuciones Linux en discos externos desde Windows.
package main

import (
	"os"

	"distrogo/internal/cli"
	"distrogo/pkg/windisk"
)

func main() {
	app := &cli.App{
		Out:             os.Stdout,
		Err:             os.Stderr,
		Enumerate:       windisk.Enumerate,
		ProtectedMounts: cli.SystemMounts(),
	}
	os.Exit(app.Run(os.Args[1:]))
}
