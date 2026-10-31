//go:build windows

package cli

import (
	"errors"
	"flag"
	"fmt"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/gpt"
	"distrogo/pkg/safety"
	"distrogo/pkg/windisk"
)

// runInstall prepara la instalacion sobre un disco fisico.
//
// En este paso solo planifica: comprueba la identidad del disco, lo abre en
// exclusiva, bloquea sus volumenes, lee la tabla que tenga y dice lo que se va a
// hacer. No escribe un byte. La escritura llega en el paso siguiente, y por eso
// existe --apply: sin el, este comando es seguro de ejecutar.
func (a *App) runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	target := fs.Int("target-disk", -1, "numero de disco fisico, el que muestra list")
	expectSerial := fs.String("expect-serial", "", "serial del disco; si no coincide no se toca nada")
	allowNonUSB := fs.Bool("allow-non-usb", false, "aceptar un disco que no sea USB")
	espFlag := fs.String("esp", "512MiB", "tamano de la particion EFI")
	apply := fs.Bool("apply", false, "escribir de verdad (todavia no implementado)")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Err, "argumentos de mas: %v\n", fs.Args())
		return exitUsage
	}
	if *target < 0 {
		fmt.Fprintln(a.Err, "error: falta --target-disk")
		return exitUsage
	}
	if *apply {
		fmt.Fprintln(a.Err, "error: escribir en un disco fisico todavia no esta implementado; este comando solo muestra el plan")
		return exitUsage
	}

	espSize, err := parseSize(*espFlag)
	if err != nil {
		fmt.Fprintf(a.Err, "error: --esp: %v\n", err)
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

	d, ok := findDisk(inv.Disks, uint32(*target))
	if !ok {
		fmt.Fprintf(a.Err, "error: no existe el disco %d; estan el %s\n",
			*target, diskNumbers(inv.Disks))
		return exitError
	}

	// La autorizacion va antes de abrir nada: comprueba el disco del sistema y el
	// serial. Si no pasa, no se toca el hardware ni para leer.
	if err := safety.Authorize(d, safety.Request{
		DiskNumber:           d.Number,
		ExpectSerial:         *expectSerial,
		AllowNonUSB:          *allowNonUSB,
		ProtectedMountPoints: a.ProtectedMounts,
	}); err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			fmt.Fprintf(a.Err, "error: no se puede instalar en el disco %d\n", d.Number)
			for _, r := range refusal.Reasons {
				fmt.Fprintf(a.Err, "  - %s\n", r.Detail)
			}
			fmt.Fprintln(a.Err, "\nNo se ha abierto el disco ni se ha escrito nada.")
			return exitError
		}
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	a.writeDiskPlan(d, espSize)

	// Comprobar la elevacion antes de tocar el hardware, para que el error llegue
	// con la instruccion de como arreglarlo y no como un fallo de ioctl.
	if !windisk.IsElevated() {
		fmt.Fprintln(a.Err, "error: para abrir el disco hace falta una consola elevada")
		fmt.Fprintln(a.Err, "       abre PowerShell como administrador y repite el comando")
		fmt.Fprintln(a.Err, "       no se ha abierto el disco ni se ha escrito nada")
		return exitError
	}

	// A partir de aqui se toca el hardware, aunque solo sea para leer.
	disk, err := windisk.OpenExclusive(int(d.Number))
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}
	defer func() { _ = disk.Close() }()

	for _, path := range disk.LockedVolumes() {
		a.phase("volumen bloqueado y desmontado: %s", path)
	}
	for _, path := range disk.OfflineVolumes() {
		a.phase("volumen %s fuera de linea: otro programa lo tenia abierto", path)
	}
	a.phase("disco %d abierto en exclusiva, %s, sectores de %d",
		disk.Number(), blockdev.HumanSize(disk.Size()), disk.SectorSize())

	table, layout, err := gpt.Read(disk)
	if err != nil {
		a.phase("no hay una tabla GPT legible (%v): se creara una nueva", err)
	} else {
		usables := int64(layout.LastUsable-layout.FirstUsable+1) * int64(layout.SectorSize)
		a.phase("tabla actual: %d particiones, area util de %s desde el LBA %d",
			len(table.Partitions), blockdev.HumanSize(usables), layout.FirstUsable)
		for i, p := range table.Partitions {
			a.phase("  %d  %-10s %s  LBA %d..%d  %s",
				i+1, p.Name, blockdev.HumanSize(p.Bytes(layout.SectorSize)),
				p.FirstLBA, p.LastLBA, p.Type)
		}
	}

	a.phase("lectura completada; no se ha escrito nada")
	return exitOK
}

// writeDiskPlan imprime que disco se va a usar y que se va a destruir.
func (a *App) writeDiskPlan(d blockdev.Disk, espSize int64) {
	fmt.Fprintf(a.Out, "disco      : %d  %s [%s]\n", d.Number, d.Model, d.Bus)
	fmt.Fprintf(a.Out, "serial     : %s\n", d.Serial)
	fmt.Fprintf(a.Out, "tamano     : %s (%d sectores de %d)\n",
		blockdev.HumanSize(d.SizeBytes), d.SizeBytes/int64(d.LogicalSector), d.LogicalSector)
	fmt.Fprintf(a.Out, "ESP        : %s\n", blockdev.HumanSize(espSize))
	fmt.Fprintf(a.Out, "root       : %s (el resto)\n", blockdev.HumanSize(d.SizeBytes-espSize))

	if len(d.Partitions) == 0 {
		fmt.Fprintln(a.Out, "particiones : ninguna")
		return
	}

	fmt.Fprintln(a.Out, "se destruye :")
	for _, p := range d.Partitions {
		detalle := p.TypeGUID
		if p.Volume != nil && p.Volume.Mounted() {
			detalle = fmt.Sprintf("%s, montada en %s", p.Volume.FileSystem, p.Volume.MountPoint)
			if p.Volume.Label != "" {
				detalle += fmt.Sprintf(" (%q)", p.Volume.Label)
			}
		}
		fmt.Fprintf(a.Out, "  %d  %s  %s\n", p.Number, blockdev.HumanSize(p.SizeBytes), detalle)
	}
}

// findDisk busca un disco por su numero.
func findDisk(disks []blockdev.Disk, number uint32) (blockdev.Disk, bool) {
	for _, d := range disks {
		if d.Number == number {
			return d, true
		}
	}
	return blockdev.Disk{}, false
}

// diskNumbers lista los discos disponibles para el mensaje de error.
func diskNumbers(disks []blockdev.Disk) string {
	if len(disks) == 0 {
		return "ninguno"
	}
	out := ""
	for i, d := range disks {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%d", d.Number)
	}
	return out
}
