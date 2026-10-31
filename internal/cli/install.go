package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"distrogo/pkg/bootcfg"
	"distrogo/pkg/efilayout"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
)

// installTarget deja el disco arrancando el sistema instalado, no el live.
//
// Copia al ESP el kernel y el initrd que ya estan dentro del rootfs, escribe un
// grub.cfg que apunta a la particion root por UUID y anade el /etc/fstab del
// sistema instalado.
func (a *App) installTarget(esp *fat32.FileSystem, root *ext4fs.FS, espInfo fat32.Info, rootInfo ext4fs.Info, title string) (efilayout.Result, error) {
	var res efilayout.Result

	kernel, initrd, err := pickKernel(root)
	if err != nil {
		return res, err
	}

	for _, path := range []string{kernel, initrd} {
		rc, size, err := root.OpenFile(path)
		if err != nil {
			return res, err
		}
		werr := esp.ReplaceFile(path, rc)
		_ = rc.Close()
		if werr != nil {
			return res, fmt.Errorf("copiar %q al ESP: %w", path, werr)
		}
		res.Files++
		res.Bytes += size
	}

	// El kernel arranca el sistema del disco: root por UUID, en lectura-escritura.
	// Ojo con el prefijo: sin "root=" el parametro UUID= es invisible para el
	// kernel y el initramfs se queda sin raiz.
	args := []string{"root=" + bootcfg.UUIDSpec(rootInfo.UUID), "rw", "quiet", "splash"}
	cfg := efilayout.GrubCfg(title, args, kernel, initrd)
	if err := esp.ReplaceFile("boot/grub/grub.cfg", strings.NewReader(cfg)); err != nil {
		return res, fmt.Errorf("escribir el grub.cfg: %w", err)
	}
	res.Files++
	res.Bytes += int64(len(cfg))

	fstab, err := bootcfg.Fstab([]bootcfg.Mount{
		{Spec: bootcfg.UUIDSpec(rootInfo.UUID), Dir: "/", Type: "ext4", Options: "errors=remount-ro", Pass: 1},
		{Spec: bootcfg.UUIDSpec(espInfo.UUID()), Dir: "/boot/efi", Type: "vfat", Options: "umask=0077", Pass: 2},
	})
	if err != nil {
		return res, err
	}
	if err := root.ReplaceFile("etc/fstab", strings.NewReader(fstab)); err != nil {
		return res, fmt.Errorf("escribir el fstab: %w", err)
	}

	return res, nil
}

// pickKernel busca en /boot el par kernel e initrd mas moderno.
func pickKernel(root *ext4fs.FS) (kernel, initrd string, err error) {
	entries, err := root.List("boot")
	if err != nil {
		return "", "", fmt.Errorf("listar /boot: %w", err)
	}

	var versions []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if v, ok := strings.CutPrefix(e.Name, "vmlinuz-"); ok {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return "", "", errors.New("no hay ningun vmlinuz en /boot: el rootfs no parece un sistema instalable")
	}

	// De mayor a menor: nos quedamos con el kernel mas nuevo que tenga initrd.
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	for _, v := range versions {
		k := "boot/vmlinuz-" + v
		i := "boot/initrd.img-" + v
		if _, err := root.Stat(i); err != nil {
			continue
		}
		return k, i, nil
	}
	return "", "", fmt.Errorf("hay kernel (%s) pero ninguno tiene initrd", strings.Join(versions, ", "))
}
