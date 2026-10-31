package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"distrogo/pkg/safety"
	"distrogo/pkg/windisk"
)

// App agrupa las dependencias del CLI para poder testearlo sin hardware.
type App struct {
	// Out y Err son la salida estandar y la de errores.
	Out io.Writer
	Err io.Writer
	// Enumerate devuelve el inventario de discos. En produccion es
	// windisk.Enumerate; en tests, un inventario falso.
	Enumerate func() (windisk.Inventory, error)
	// ProtectedMounts son los puntos de montaje que nunca se pueden tocar.
	// En produccion, SystemMounts().
	ProtectedMounts []string
}

// Codigos de salida.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Run ejecuta el CLI y devuelve el codigo de salida.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.printUsage()
		return exitUsage
	}

	switch args[0] {
	case "list":
		return a.runList(args[1:])
	// TODO(DG-23): el comando de particionado sobre imagen, que junta NewLayout,
	// Plan y Commit. Es el criterio de salida de M2.
	case "help", "-h", "--help":
		a.printUsage()
		return exitOK
	default:
		fmt.Fprintf(a.Err, "comando desconocido: %q\n\n", args[0])
		a.printUsage()
		return exitUsage
	}
}

func (a *App) runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	asJSON := fs.Bool("json", false, "salida JSON para scripts")
	allowNonUSB := fs.Bool("allow-non-usb", false, "no avisar de discos que no estan en USB")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Err, "argumentos de mas: %v\n", fs.Args())
		return exitUsage
	}

	if a.Enumerate == nil {
		fmt.Fprintln(a.Err, "error: falta el enumerador de discos")
		return exitError
	}

	inv, err := a.Enumerate()
	if err != nil {
		fmt.Fprintf(a.Err, "error: leer el inventario de discos: %v\n", err)
		return exitError
	}

	verdicts := safety.AssessAll(inv.Disks, safety.Options{
		AllowNonUSB:          *allowNonUSB,
		ProtectedMountPoints: a.ProtectedMounts,
	})

	if *asJSON {
		if err := a.writeJSON(inv, verdicts); err != nil {
			fmt.Fprintf(a.Err, "error: escribir el JSON: %v\n", err)
			return exitError
		}
		return exitOK
	}

	a.writeHuman(inv, verdicts)
	return exitOK
}

func (a *App) printUsage() {
	fmt.Fprint(a.Out, `distrogo - instalador de Linux en discos externos desde Windows

Uso:
  distrogo list [--json] [--allow-non-usb]

Comandos:
  list    discos de la maquina, su estado y lo que se destruiria al escribir

Flags de list:
  --json            salida JSON para scripts
  --allow-non-usb   no avisar de discos que no estan conectados por USB

El comando install llega en M6. Hasta entonces, todo se valida contra imagenes.
`)
}

// SystemMounts devuelve los puntos de montaje que nunca se pueden tocar. Hoy es
// la unidad donde esta instalado Windows.
func SystemMounts() []string {
	root := os.Getenv("SystemRoot")
	if len(root) >= 2 && root[1] == ':' {
		return []string{root[:2] + `\`}
	}
	return nil
}
