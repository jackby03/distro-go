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
	case "partition":
		return a.runPartition(args[1:])
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
  distrogo partition --image <ruta> [--size 8GiB] [--esp 512MiB] [--force]

Comandos:
  list       discos de la maquina, su estado y lo que se destruiria al escribir
  partition  crea una imagen de disco con tabla GPT, ESP y root

Flags de list:
  --json            salida JSON para scripts
  --allow-non-usb   no avisar de discos que no estan conectados por USB

Flags de partition:
  --image   ruta de la imagen a crear (obligatorio)
  --size    tamano de la imagen, por defecto 8GiB
  --esp     tamano de la particion EFI, por defecto 512MiB
  --force   sobrescribir la imagen si ya existe
  --format-esp  formatear la particion EFI en FAT32 (por defecto si)
  --format-root formatear la particion root en ext4 (por defecto si)
  --boot-from   ISO del que copiar el arranque al ESP
  --with-squashfs  copiar tambien el rootfs comprimido (ESP grande)
  --kernel-args parametros de arranque del kernel

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
