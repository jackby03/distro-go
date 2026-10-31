// Package efilayout compone el arbol de arranque en el ESP.
//
// Copia del ISO el cargador EFI que firma la distribucion, los modulos de GRUB y
// el kernel con su initrd, y escribe el grub.cfg de la instalacion.
//
// No fabrica binarios EFI: reutiliza el shim y el GRUB firmados por la
// distribucion, que es lo unico que funciona con Secure Boot.
package efilayout

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"distrogo/pkg/fat32"
	"distrogo/pkg/linuxiso"
)

// ErrNotEnoughSpace indica que el ESP no tiene hueco para lo que se quiere copiar.
var ErrNotEnoughSpace = errors.New("el ESP no tiene espacio para el contenido de arranque")

// Spec describe que se copia y como se arranca.
type Spec struct {
	// Title es el texto del menu de GRUB.
	Title string
	// KernelDest e InitrdDest son las rutas de destino dentro del ESP. Vacio usa
	// las mismas que trae el ISO.
	KernelDest string
	InitrdDest string
	// KernelArgs son los parametros de arranque.
	KernelArgs []string
	// CopySquashfs copia ademas el rootfs comprimido. Solo tiene sentido cuando
	// el ESP es lo bastante grande para el sistema live completo.
	CopySquashfs bool
	// GrubModules copia el arbol de modulos de GRUB. Hace falta si el
	// grubx64.efi del ISO no es autosuficiente, cosa que solo se sabe arrancando;
	// por defecto se copia.
	SkipGrubModules bool
}

// Result resume lo que se ha copiado.
type Result struct {
	Files int
	Bytes int64
}

type planned struct {
	src  string
	dst  string
	size int64
}

// Write copia el contenido de arranque del ISO al ESP y escribe el grub.cfg.
//
// El grub.cfg se escribe el ultimo: si algo falla antes, el ESP no queda
// arrancable a medias.
func Write(iso *linuxiso.ISO, esp *fat32.FileSystem, spec Spec) (Result, error) {
	var res Result

	title := strings.TrimSpace(spec.Title)
	if title == "" {
		title = "DistroGo"
	}

	facts, err := iso.Facts()
	if err != nil {
		return res, err
	}
	if facts.EFIDir == "" {
		return res, fmt.Errorf("el ISO no trae cargador EFI en EFI/boot")
	}

	kernelDest := spec.KernelDest
	if kernelDest == "" {
		kernelDest = facts.Kernel
	}
	initrdDest := spec.InitrdDest
	if initrdDest == "" {
		initrdDest = facts.Initrd
	}

	items, err := plan(iso, facts, spec, kernelDest, initrdDest)
	if err != nil {
		return res, err
	}

	var needed int64
	for _, it := range items {
		needed += it.size
	}
	if free := esp.FreeBytes(); needed > free {
		return res, fmt.Errorf("%w: hacen falta %d bytes y quedan %d", ErrNotEnoughSpace, needed, free)
	}

	for _, it := range items {
		if err := copyOne(iso, esp, it, &res); err != nil {
			return res, err
		}
	}

	cfg := GrubCfg(title, spec.KernelArgs, kernelDest, initrdDest)
	if err := esp.WriteFile(grubCfgPath, strings.NewReader(cfg)); err != nil {
		return res, fmt.Errorf("escribir %s: %w", grubCfgPath, err)
	}
	res.Files++
	res.Bytes += int64(len(cfg))

	return res, nil
}

const grubCfgPath = "boot/grub/grub.cfg"

// plan decide que ficheros hay que copiar y cuanto ocupan.
func plan(iso *linuxiso.ISO, facts linuxiso.Facts, spec Spec, kernelDest, initrdDest string) ([]planned, error) {
	var items []planned

	add := func(src, dst string) error {
		size, err := iso.SizeOf(src)
		if err != nil {
			return fmt.Errorf("medir %q: %w", src, err)
		}
		items = append(items, planned{src: src, dst: dst, size: size})
		return nil
	}

	// Arbol EFI completo: shim, GRUB y lo que la distribucion ponga al lado.
	if err := addTree(iso, facts.EFIDir, facts.EFIDir, add); err != nil {
		return nil, err
	}

	if !spec.SkipGrubModules {
		if err := addTree(iso, "boot/grub/x86_64-efi", "boot/grub/x86_64-efi", add); err != nil {
			return nil, err
		}
	}

	if err := add(facts.Kernel, kernelDest); err != nil {
		return nil, err
	}
	if err := add(facts.Initrd, initrdDest); err != nil {
		return nil, err
	}

	if spec.CopySquashfs {
		if err := add(facts.Squashfs, facts.Squashfs); err != nil {
			return nil, err
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].dst < items[j].dst })
	return items, nil
}

// addTree recorre un directorio del ISO y anade sus ficheros.
func addTree(iso *linuxiso.ISO, prefix, destPrefix string, add func(src, dst string) error) error {
	if !iso.Exists(prefix) {
		return nil
	}
	return iso.Walk(prefix, func(e linuxiso.Entry) error {
		if e.IsDir {
			return nil
		}
		rel := strings.TrimPrefix(e.Path, prefix)
		rel = strings.TrimPrefix(rel, "/")
		return add(e.Path, path.Join(destPrefix, rel))
	})
}

// copyOne copia un fichero del ISO al ESP.
func copyOne(iso *linuxiso.ISO, esp *fat32.FileSystem, it planned, res *Result) error {
	rc, _, err := iso.OpenFile(it.src)
	if err != nil {
		return fmt.Errorf("abrir %q: %w", it.src, err)
	}
	// Un handle por fichero: el defer se ejecuta al volver de copyOne.
	defer rc.Close()

	if err := esp.WriteFile(it.dst, rc); err != nil {
		return fmt.Errorf("copiar %q a %q: %w", it.src, it.dst, err)
	}
	res.Files++
	res.Bytes += it.size
	return nil
}

// GrubCfg genera el menu de arranque.
//
// Las rutas van con barra inicial porque GRUB las resuelve contra su prefijo, que
// en este montaje es la raiz del ESP.
func GrubCfg(title string, args []string, kernel, initrd string) string {
	var b strings.Builder

	b.WriteString("# Generado por DistroGo. No editar a mano.\n")
	b.WriteString("set default=0\n")
	b.WriteString("set timeout=3\n\n")

	fmt.Fprintf(&b, "menuentry %q {\n", title)
	fmt.Fprintf(&b, "\tlinux /%s", strings.TrimPrefix(kernel, "/"))
	for _, a := range args {
		if a == "" {
			continue
		}
		b.WriteString(" ")
		b.WriteString(a)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "\tinitrd /%s\n", strings.TrimPrefix(initrd, "/"))
	b.WriteString("}\n")

	return b.String()
}
