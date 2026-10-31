package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/fat32"
	"distrogo/pkg/gpt"
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

	entries, err := gpt.Plan(layout, []gpt.SizeSpec{
		{Name: "ESP", Type: gpt.TypeESP, SizeBytes: espSize},
		{Name: "root", Type: gpt.TypeLinuxFilesystem},
	})
	if err != nil {
		fmt.Fprintf(a.Err, "error: repartir: %v\n", err)
		return exitError
	}

	table, err := gpt.NewTable(gpt.GUID{}, entries)
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	if err := gpt.Commit(img, table, layout); err != nil {
		fmt.Fprintf(a.Err, "error: escribir la tabla GPT: %v\n", err)
		return exitError
	}

	// Verificacion con nuestro propio lector: si algo no cuadra, mejor saberlo
	// aqui que en el invitado de QEMU.
	back, _, err := gpt.Read(img)
	if err != nil {
		fmt.Fprintf(a.Err, "error: verificar la tabla escrita: %v\n", err)
		return exitError
	}
	if err := sameTable(table, back); err != nil {
		fmt.Fprintf(a.Err, "error: la tabla releida no coincide: %v\n", err)
		return exitError
	}

	// La ESP es una particion, asi que se formatea a traves de una vista
	// recortada del dispositivo. Es el mismo camino que usara el root en M5.
	var espInfo *fat32.Info
	if *formatESP {
		esp := table.Partitions[0]
		offset := int64(esp.FirstLBA) * int64(layout.SectorSize)
		section, secErr := blockdev.NewSection(img, offset, esp.Bytes(layout.SectorSize))
		if secErr != nil {
			fmt.Fprintf(a.Err, "error: preparar la ESP: %v\n", secErr)
			return exitError
		}
		vol, fmtErr := fat32.Format(section, fat32.Options{Label: "EFI"})
		if fmtErr != nil {
			fmt.Fprintf(a.Err, "error: formatear la ESP: %v\n", fmtErr)
			return exitError
		}
		info := vol.Info()
		espInfo = &info
	}

	a.writePartitionSummary(*imagePath, size, img.SectorSize(), layout, table, espInfo)
	return exitOK
}

func (a *App) writePartitionSummary(path string, size int64, sectorSize int, l gpt.Layout, t *gpt.Table, esp *fat32.Info) {
	fmt.Fprintf(a.Out, "imagen     : %s (%s, %d sectores de %d)\n",
		path, blockdev.HumanSize(size), l.TotalSectors, sectorSize)
	fmt.Fprintf(a.Out, "disco GUID : %s\n", t.DiskGUID)
	fmt.Fprintf(a.Out, "area util  : LBA %d..%d\n", l.FirstUsable, l.LastUsable)
	fmt.Fprintf(a.Out, "particiones: %d\n", len(t.Partitions))
	for i, p := range t.Partitions {
		fmt.Fprintf(a.Out, "  %d  %-4s %10s  LBA %d..%d  %s  %s\n",
			i+1, p.Name, blockdev.HumanSize(p.Bytes(sectorSize)),
			p.FirstLBA, p.LastLBA, p.Type, p.Unique)
	}
	if esp != nil {
		fmt.Fprintf(a.Out, "ESP        : FAT32, cluster de %d bytes, %d clusters, etiqueta %s\n",
			esp.ClusterSize, esp.Clusters, esp.Label)
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
