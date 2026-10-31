package linuxiso

import (
	"errors"
	"fmt"
	"strings"
)

// Variant identifica la disposicion de un sistema live.
type Variant string

const (
	// VariantCasper es la de Ubuntu y Linux Mint: casper/vmlinuz, initrd y
	// filesystem.squashfs.
	VariantCasper Variant = "casper"
	// VariantLive es la de Debian y derivados: live/vmlinuz, live/initrd.img y
	// live/filesystem.squashfs.
	VariantLive Variant = "live"
	// VariantLiveOS es la de Fedora y derivados: LiveOS/squashfs.img.
	VariantLiveOS Variant = "LiveOS"
)

// ErrNotLive indica que el ISO no tiene una disposicion live reconocible.
var ErrNotLive = errors.New("el ISO no contiene un rootfs live reconocible")

// Facts resume lo que necesita el instalador de un ISO live.
//
// Las rutas son las canonicas del ISO, no las mayusculas reales: la busqueda no
// distingue mayusculas y el resto del codigo tampoco necesita hacerlo.
type Facts struct {
	Label        string
	Variant      Variant
	Kernel       string
	Initrd       string
	Squashfs     string
	SquashfsSize int64
	// Manifest es el listado de paquetes, si existe. Sirve para verificar mas
	// adelante que el arbol extraido coincide.
	Manifest string
	// EFIDir es el directorio con el cargador EFI que trae la distribucion.
	EFIDir string
	// GrubCfg es el grub.cfg del ISO, el que hay que reescribir para la
	// instalacion.
	GrubCfg string
}

// layout describe donde busca cada variante sus piezas.
type layout struct {
	variant  Variant
	squashfs string
	manifest string
	kernels  []string
	initrds  []string
}

var layouts = []layout{
	{
		variant:  VariantCasper,
		squashfs: "casper/filesystem.squashfs",
		manifest: "casper/filesystem.manifest",
		kernels:  []string{"casper/vmlinuz"},
		initrds:  []string{"casper/initrd.lz", "casper/initrd.gz", "casper/initrd.img"},
	},
	{
		variant:  VariantLive,
		squashfs: "live/filesystem.squashfs",
		manifest: "live/filesystem.manifest",
		kernels:  []string{"live/vmlinuz", "live/vmlinuz.efi"},
		initrds:  []string{"live/initrd.img", "live/initrd.gz"},
	},
	{
		variant:  VariantLiveOS,
		squashfs: "LiveOS/squashfs.img",
		kernels:  []string{"LiveOS/vmlinuz"},
		initrds:  []string{"LiveOS/initrd.img"},
	},
}

// probe dice si una ruta existe y cuanto ocupa.
type probe func(name string) (int64, bool)

// detect elige la primera variante cuyas tres piezas existen.
func detect(label string, lookup probe) (Facts, error) {
	for _, l := range layouts {
		squashSize, ok := lookup(l.squashfs)
		if !ok {
			continue
		}
		kernel, ok := firstExisting(lookup, l.kernels)
		if !ok {
			continue
		}
		initrd, ok := firstExisting(lookup, l.initrds)
		if !ok {
			continue
		}

		f := Facts{
			Label:        label,
			Variant:      l.variant,
			Kernel:       kernel,
			Initrd:       initrd,
			Squashfs:     l.squashfs,
			SquashfsSize: squashSize,
		}
		if _, ok := lookup(l.manifest); ok {
			f.Manifest = l.manifest
		}
		if _, ok := lookup("EFI/boot/bootx64.efi"); ok {
			f.EFIDir = "EFI/boot"
		}
		if _, ok := lookup("boot/grub/grub.cfg"); ok {
			f.GrubCfg = "boot/grub/grub.cfg"
		}
		return f, nil
	}

	return Facts{Label: label}, fmt.Errorf("%w: se busco %s",
		ErrNotLive, strings.Join(variantNames(), ", "))
}

// variantNames lista las variantes conocidas para el mensaje de error.
func variantNames() []string {
	out := make([]string, 0, len(layouts))
	for _, l := range layouts {
		out = append(out, fmt.Sprintf("%s (%s)", l.variant, l.squashfs))
	}
	return out
}

func firstExisting(lookup probe, names []string) (string, bool) {
	for _, n := range names {
		if _, ok := lookup(n); ok {
			return n, true
		}
	}
	return "", false
}

// Facts analiza el ISO abierto.
func (i *ISO) Facts() (Facts, error) {
	label, err := i.Label()
	if err != nil {
		return Facts{}, err
	}
	return detect(label, func(name string) (int64, bool) {
		size, err := i.SizeOf(name)
		if err != nil {
			return 0, false
		}
		return size, true
	})
}
