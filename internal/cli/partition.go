package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/efilayout"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
	"distrogo/pkg/gpt"
	"distrogo/pkg/linuxiso"
	"distrogo/pkg/rootfs"
	"distrogo/pkg/squashfs"
)

// runPartition crea una imagen de disco y le escribe una tabla GPT con dos
// particiones: ESP y root. Es el ensayo de M2: mismo codigo que usara el
// instalador real, pero sobre un fichero.
func (a *App) runPartition(args []string) int {
	fs := flag.NewFlagSet("partition", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	imagePath := fs.String("image", "", "ruta de la imagen a crear (obligatorio)")
	sizeFlag := fs.String("size", "8GiB", "tamano de la imagen")
	espFlag := fs.String("esp", "512MiB", "tamano de la particion EFI")
	force := fs.Bool("force", false, "sobrescribir la imagen si ya existe")
	formatESP := fs.Bool("format-esp", true, "formatear la particion EFI en FAT32")
	formatRoot := fs.Bool("format-root", true, "formatear la particion root en ext4")
	populate := fs.Bool("populate", false, "volcar el rootfs del ISO en la particion root")
	install := fs.Bool("install", false, "dejar el disco arrancando el sistema instalado (implica --populate)")
	title := fs.String("title", "", "texto del menu de arranque (por defecto, el nombre de la distro)")
	bootFrom := fs.String("boot-from", "", "ISO del que copiar el arranque al ESP")
	withSquashfs := fs.Bool("with-squashfs", false, "copiar tambien el rootfs comprimido al ESP")
	kernelArgs := fs.String("kernel-args", "", "parametros de arranque del kernel, separados por espacios")
	userName := fs.String("user", "", "nombre de la cuenta que se crea en el sistema instalado")
	userUID := fs.Int("uid", 1000, "UID de esa cuenta")
	passwordHash := fs.String("password-hash", "", "hash SHA-512 ($6$...) de la contrasena, para uso no interactivo")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Err, "argumentos de mas: %v\n", fs.Args())
		return exitUsage
	}
	if *imagePath == "" {
		fmt.Fprintln(a.Err, "error: falta --image")
		return exitUsage
	}
	if *userName != "" && !*install {
		fmt.Fprintln(a.Err, "error: --user necesita --install, la cuenta se crea en el sistema instalado")
		return exitUsage
	}
	if *install {
		*populate = true
	}

	size, err := parseSize(*sizeFlag)
	if err != nil {
		fmt.Fprintf(a.Err, "error: --size: %v\n", err)
		return exitUsage
	}
	espSize, err := parseSize(*espFlag)
	if err != nil {
		fmt.Fprintf(a.Err, "error: --esp: %v\n", err)
		return exitUsage
	}

	if inUse, err := blockdev.InUse(*imagePath); err == nil && inUse {
		fmt.Fprintf(a.Err, "error: %s esta abierta por otro proceso (QEMU?); cierrala antes de reescribirla\n", *imagePath)
		return exitError
	}

	if !*force {
		if st, err := os.Stat(*imagePath); err == nil && st.Size() > 0 {
			fmt.Fprintf(a.Err, "error: %s ya existe (%s); usa --force para sobrescribirla\n",
				*imagePath, blockdev.HumanSize(st.Size()))
			return exitError
		}
	}

	img, err := blockdev.CreateImage(*imagePath, size)
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}
	// Un solo handle: el defer se ejecuta al salir de runPartition.
	defer img.Close()

	layout, err := gpt.NewLayout(size, img.SectorSize())
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	opts := partitionOptions{
		espSize:       espSize,
		formatESP:     *formatESP,
		formatRoot:    *formatRoot,
		populate:      *populate,
		install:       *install,
		bootFrom:      *bootFrom,
		withSquashfs:  *withSquashfs,
		kernelArgs:    *kernelArgs,
		title:         *title,
		fallbackTitle: "DistroGo - " + *imagePath,
		userName:      *userName,
		userUID:       *userUID,
		passwordHash:  *passwordHash,
	}

	res, err := a.applyPartition(img, layout, opts)
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	a.writePartitionSummary(summaryData{
		target:     *imagePath,
		size:       size,
		sectorSize: img.SectorSize(),
		layout:     layout,
		table:      res.table,
		esp:        res.espInfo,
		content:    res.espContent,
		root:       res.rootInfo,
		stats:      res.rootStats,
	})

	if res.accountInfo != nil {
		fmt.Fprintf(a.Out, "cuenta     : %s (uid %d), home %s, grupo %s, %d ficheros de skel, %d entradas del home a su nombre\n",
			res.accountInfo.User, res.accountInfo.UID, res.accountInfo.Home,
			res.accountInfo.AdminGroup, res.accountInfo.HomeFiles, res.accountInfo.HomeOwned)
	}
	return exitOK
}

// openSquashfs abre el rootfs comprimido de un ISO sin extraerlo.
//
// Devuelve tambien el ISO: el lector de squashfs lee del fichero, asi que hay que
// mantenerlo abierto hasta que se termine de volcar. Cerrarlo aqui fue un fallo
// que solo aparece al volcar de verdad.
func openSquashfs(isoPath string) (*squashfs.FS, *linuxiso.ISO, error) {
	iso, err := linuxiso.Open(isoPath)
	if err != nil {
		return nil, nil, err
	}

	facts, err := iso.Facts()
	if err != nil {
		_ = iso.Close()
		return nil, nil, err
	}
	ra, _, err := iso.ReaderAt(facts.Squashfs)
	if err != nil {
		_ = iso.Close()
		return nil, nil, err
	}

	src, err := squashfs.Open(ra)
	if err != nil {
		_ = iso.Close()
		return nil, nil, err
	}
	return src, iso, nil
}

// populateESP copia al ESP el arranque del ISO y su grub.cfg.
func (a *App) populateESP(vol *fat32.FileSystem, isoPath string, withSquashfs bool, kernelArgs string, skipKernel bool) (efilayout.Result, error) {
	iso, err := linuxiso.Open(isoPath)
	if err != nil {
		return efilayout.Result{}, err
	}
	defer iso.Close()

	facts, err := iso.Facts()
	if err != nil {
		return efilayout.Result{}, err
	}

	var args []string
	if s := strings.TrimSpace(kernelArgs); s != "" {
		args = strings.Fields(s)
	}

	res, err := efilayout.Write(iso, vol, efilayout.Spec{
		Title:               facts.Label,
		KernelArgs:          args,
		CopySquashfs:        withSquashfs,
		SkipKernelAndInitrd: skipKernel,
	})
	if err != nil {
		return efilayout.Result{}, err
	}
	return res, nil
}

// summaryData reune todo lo que pinta el resumen final de una instalacion.
//
// En su dia fue una lista de nueve parametros posicionales. Un struct deja claro
// que es cada cosa en las dos llamadas que hay (imagen y disco fisico) y evita
// que manana alguien pase el ESP donde va el root.
type summaryData struct {
	// target es lo que se ensena en la linea "imagen" o "disco".
	target     string
	size       int64
	sectorSize int
	layout     gpt.Layout
	table      *gpt.Table
	esp        *fat32.Info
	content    *efilayout.Result
	root       *ext4fs.Info
	stats      *rootfs.Stats
}

func (a *App) writePartitionSummary(d summaryData) {
	if d.root != nil {
		fmt.Fprintf(a.Out, "root       : ext4, bloque %d, %d inodos, UUID %s, libre %s\n",
			d.root.BlockSize, d.root.InodeCount, d.root.UUID, blockdev.HumanSize(d.root.FreeBytes))
	}
	if d.stats != nil {
		fmt.Fprintf(a.Out, "volcado    : %d dirs, %d ficheros, %d symlinks, %d hardlinks, %d bytes\n",
			d.stats.Dirs, d.stats.Files, d.stats.Symlinks, d.stats.Hardlinks, d.stats.Bytes)
		if d.stats.Unsupported > 0 {
			fmt.Fprintf(a.Out, "             %d entradas sin soporte en ext4 (device nodes y hardlinks duplicados)\n",
				d.stats.Unsupported)
		}
	}
	fmt.Fprintf(a.Out, "imagen     : %s (%s, %d sectores de %d)\n",
		d.target, blockdev.HumanSize(d.size), d.layout.TotalSectors, d.sectorSize)
	fmt.Fprintf(a.Out, "disco GUID : %s\n", d.table.DiskGUID)
	fmt.Fprintf(a.Out, "area util  : LBA %d..%d\n", d.layout.FirstUsable, d.layout.LastUsable)
	fmt.Fprintf(a.Out, "particiones: %d\n", len(d.table.Partitions))
	for i, p := range d.table.Partitions {
		fmt.Fprintf(a.Out, "  %d  %-4s %10s  LBA %d..%d  %s  %s\n",
			i+1, p.Name, blockdev.HumanSize(p.Bytes(d.sectorSize)),
			p.FirstLBA, p.LastLBA, p.Type, p.Unique)
	}
	if d.esp != nil {
		fmt.Fprintf(a.Out, "ESP        : FAT32, cluster de %d bytes, %d clusters, etiqueta %s\n",
			d.esp.ClusterSize, d.esp.Clusters, d.esp.Label)
	}
	if d.content != nil {
		fmt.Fprintf(a.Out, "arranque   : %d ficheros, %s copiados al ESP\n",
			d.content.Files, blockdev.HumanSize(d.content.Bytes))
	}
	fmt.Fprintln(a.Out, "verificado  : firma, CRC de cabecera y CRC de entradas correctos")
}

// sameTable compara lo que se quiso escribir con lo que se releyo.
func sameTable(want, got *gpt.Table) error {
	if want.DiskGUID != got.DiskGUID {
		return fmt.Errorf("GUID del disco %s, releido %s", want.DiskGUID, got.DiskGUID)
	}
	if len(want.Partitions) != len(got.Partitions) {
		return fmt.Errorf("%d particiones, releidas %d", len(want.Partitions), len(got.Partitions))
	}
	for i := range want.Partitions {
		if want.Partitions[i] != got.Partitions[i] {
			return fmt.Errorf("particion %d: escrita %+v, releida %+v", i+1, want.Partitions[i], got.Partitions[i])
		}
	}
	return nil
}

// parseSize acepta "8GiB", "512MiB", "7.5GiB" o un numero de bytes.
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	if t == "" {
		return 0, errors.New("tamano vacio")
	}

	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{
		{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
		{"B", 1},
	} {
		if strings.HasSuffix(t, u.suffix) {
			t = strings.TrimSpace(strings.TrimSuffix(t, u.suffix))
			mult = u.mult
			break
		}
	}

	n, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, fmt.Errorf("tamano invalido %q", s)
	}
	v := int64(n * float64(mult))
	if v <= 0 {
		return 0, fmt.Errorf("tamano invalido %q: debe ser mayor que cero", s)
	}
	return v, nil
}

// phase escribe una marca de progreso con la hora.
//
// Sirve para que una instalacion larga no parezca colgada: cada fase deja una
// linea con la hora a la que empieza o termina.
func (a *App) phase(format string, args ...any) {
	fmt.Fprintf(a.Err, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
