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
	apply := fs.Bool("apply", false, "escribir de verdad: destruye el contenido del disco")
	probe := fs.Bool("probe", false, "probar los IOCTL del volumen y decir cual falla, sin escribir")
	isoPath := fs.String("iso", "", "ISO del que copiar el sistema (obligatorio con --apply)")
	userName := fs.String("user", "", "nombre de la cuenta que se crea en el sistema instalado")
	userUID := fs.Int("uid", 1000, "UID de esa cuenta")
	passwordHash := fs.String("password-hash", "", "hash SHA-512 ($6$...) de la contrasena, para uso no interactivo")
	title := fs.String("title", "", "texto del menu de arranque (por defecto, el nombre de la distro)")
	kernelArgs := fs.String("kernel-args", "", "parametros del kernel para el arranque live del ISO")

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
	if *apply && *isoPath == "" {
		fmt.Fprintln(a.Err, "error: --apply necesita --iso")
		return exitUsage
	}
	if *userName != "" && !*apply {
		fmt.Fprintln(a.Err, "error: --user necesita --apply, la cuenta se crea en el sistema instalado")
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

	// Diagnostico: probar cada IOCTL por separado. Sirve cuando el antivirus bloquea
	// uno solo y no se sabe cual.
	if *probe {
		probes, err := windisk.ProbeDisk(int(d.Number))
		if err != nil {
			fmt.Fprintf(a.Err, "error: %v\n", err)
			return exitError
		}
		for _, p := range probes {
			fmt.Fprintf(a.Out, "volumen %s\n", p.Path)
			fmt.Fprintf(a.Out, "  abrir lectura+escritura : %s\n", estadoIOCTL(p.Open))
			fmt.Fprintf(a.Out, "  FSCTL_DISMOUNT_VOLUME   : %s\n", estadoIOCTL(p.Dismount))
			fmt.Fprintf(a.Out, "  FSCTL_LOCK_VOLUME       : %s\n", estadoIOCTL(p.Lock))
			// Fuera de linea solo se intenta si el bloqueo fallo, asi que si no se
			// probo no se puede informar de un resultado que no existe.
			if p.Lock != nil {
				fmt.Fprintf(a.Out, "  IOCTL_VOLUME_OFFLINE    : %s\n", estadoIOCTL(p.Offline))
				if p.Offline == nil {
					fmt.Fprintf(a.Out, "  IOCTL_VOLUME_ONLINE     : %s\n", estadoIOCTL(p.Restore))
				}
			} else {
				fmt.Fprintln(a.Out, "  IOCTL_VOLUME_OFFLINE    : no hace falta, el bloqueo basto")
			}
		}
		fmt.Fprintln(a.Out, "diagnostico terminado; no se ha escrito ningun dato")
		return exitOK
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

	if *apply {
		return a.applyToDisk(disk, partitionOptions{
			espSize:       espSize,
			formatESP:     true,
			formatRoot:    true,
			populate:      true,
			install:       true,
			bootFrom:      *isoPath,
			kernelArgs:    *kernelArgs,
			title:         *title,
			fallbackTitle: fmt.Sprintf("DistroGo - disco %d", d.Number),
			userName:      *userName,
			userUID:       *userUID,
			passwordHash:  *passwordHash,
		})
	}

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

// applyToDisk instala de verdad en un disco fisico ya abierto en exclusiva.
//
// Usa la misma tuberia que la instalacion sobre imagen: lo que se probo durante
// meses contra un .img es literalmente el mismo codigo que escribe aqui.
func (a *App) applyToDisk(disk *windisk.Disk, opts partitionOptions) int {
	layout, err := gpt.NewLayout(disk.Size(), disk.SectorSize())
	if err != nil {
		fmt.Fprintf(a.Err, "error: repartir el disco: %v\n", err)
		return exitError
	}
	if opts.espSize <= 0 || opts.espSize >= disk.Size() {
		fmt.Fprintf(a.Err, "error: la ESP (%s) no cabe en el disco (%s)\n",
			blockdev.HumanSize(opts.espSize), blockdev.HumanSize(disk.Size()))
		return exitError
	}

	res, err := a.applyPartition(disk, layout, opts)
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	// Sin este volcado el "listo" seria mentira: los datos estarian en la cache de
	// Windows y el disco se desconectaria con medio sistema dentro.
	if err := disk.Sync(); err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	a.writePartitionSummary(windisk.DiskPath(disk.Number()), disk.Size(), disk.SectorSize(),
		layout, res.table, res.espInfo, res.espContent, res.rootInfo, res.rootStats)
	if res.accountInfo != nil {
		fmt.Fprintf(a.Out, "cuenta     : %s (uid %d), home %s, grupo %s, %d ficheros de skel, %d entradas del home a su nombre\n",
			res.accountInfo.User, res.accountInfo.UID, res.accountInfo.Home,
			res.accountInfo.AdminGroup, res.accountInfo.HomeFiles, res.accountInfo.HomeOwned)
	}

	a.phase("instalacion terminada; ya se puede desconectar el disco")
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

// estadoIOCTL traduce el resultado de un IOCTL a algo legible.
func estadoIOCTL(err error) string {
	if err == nil {
		return "ok"
	}
	return "FALLO: " + err.Error()
}
