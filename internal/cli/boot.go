package cli

import (
	"flag"
	"fmt"
	"os"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
	"distrogo/pkg/gpt"
)

// runBoot deja arrancando el sistema instalado de una imagen ya poblada.
//
// Es el paso final de una instalacion, separado de partition para poder iterar
// sobre la configuracion de arranque sin volver a volcar 500.000 ficheros.
func (a *App) runBoot(args []string) int {
	fs := flag.NewFlagSet("boot", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	imagePath := fs.String("image", "", "imagen con el sistema ya instalado (obligatorio)")
	title := fs.String("title", "DistroGo", "texto del menu de GRUB")

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
	if _, err := os.Stat(*imagePath); err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}
	if inUse, err := blockdev.InUse(*imagePath); err == nil && inUse {
		fmt.Fprintf(a.Err, "error: %s esta abierta por otro proceso (QEMU?); cierrala antes\n", *imagePath)
		return exitError
	}

	img, err := blockdev.OpenImage(*imagePath, true)
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}
	defer img.Close()

	table, layout, err := gpt.Read(img)
	if err != nil {
		fmt.Fprintf(a.Err, "error: leer la tabla GPT: %v\n", err)
		return exitError
	}

	espPart, ok := findPartition(table, gpt.TypeESP)
	if !ok {
		fmt.Fprintln(a.Err, "error: la imagen no tiene particion EFI")
		return exitError
	}
	rootPart, ok := findPartition(table, gpt.TypeLinuxFilesystem)
	if !ok {
		fmt.Fprintln(a.Err, "error: la imagen no tiene particion Linux")
		return exitError
	}

	sectorSize := int64(layout.SectorSize)
	espSection, err := blockdev.NewSection(img,
		int64(espPart.FirstLBA)*sectorSize, espPart.Bytes(layout.SectorSize))
	if err != nil {
		fmt.Fprintf(a.Err, "error: preparar la ESP: %v\n", err)
		return exitError
	}
	esp, err := fat32.Open(espSection)
	if err != nil {
		fmt.Fprintf(a.Err, "error: abrir la ESP: %v\n", err)
		return exitError
	}

	root, err := ext4fs.Open(img, int64(rootPart.FirstLBA)*sectorSize, rootPart.Bytes(layout.SectorSize))
	if err != nil {
		fmt.Fprintf(a.Err, "error: abrir el root: %v\n", err)
		return exitError
	}

	rootInfo, err := root.Info()
	if err != nil {
		fmt.Fprintf(a.Err, "error: leer el root: %v\n", err)
		return exitError
	}

	res, err := a.installTarget(esp, root, esp.Info(), rootInfo, *title)
	if err != nil {
		fmt.Fprintf(a.Err, "error: instalar el arranque: %v\n", err)
		return exitError
	}

	// Primero se vuelca el root (fstab) y luego la ESP (kernel, initrd y cfg).
	if err := root.Close(); err != nil {
		fmt.Fprintf(a.Err, "error: cerrar el root: %v\n", err)
		return exitError
	}
	if err := esp.Close(); err != nil {
		fmt.Fprintf(a.Err, "error: cerrar la ESP: %v\n", err)
		return exitError
	}

	fmt.Fprintf(a.Out, "imagen  : %s\n", *imagePath)
	fmt.Fprintf(a.Out, "root    : UUID %s (%s, %d inodos)\n",
		rootInfo.UUID, blockdev.HumanSize(rootInfo.SizeBytes), rootInfo.InodeCount)
	fmt.Fprintf(a.Out, "ESP     : UUID %s, etiqueta %s\n", esp.Info().UUID(), esp.Info().Label)
	fmt.Fprintf(a.Out, "copiado : %d ficheros, %s al ESP (kernel, initrd y grub.cfg)\n",
		res.Files, blockdev.HumanSize(res.Bytes))
	fmt.Fprintf(a.Out, "fstab   : escrito en /etc/fstab con root por UUID\n")
	return exitOK
}

// findPartition busca la primera particion de ese tipo.
func findPartition(t *gpt.Table, kind gpt.GUID) (gpt.Entry, bool) {
	for _, p := range t.Partitions {
		if p.Type == kind {
			return p, true
		}
	}
	return gpt.Entry{}, false
}
