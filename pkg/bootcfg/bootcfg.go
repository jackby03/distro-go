// Package bootcfg genera los ficheros de configuracion de arranque de una
// instalacion: el /etc/fstab del sistema destino.
//
// Son funciones puras: reciben datos y devuelven texto. Asi se pueden testear sin
// disco, y el que llama decide de donde saca los UUID.
package bootcfg

import (
	"fmt"
	"strings"
)

// Mount describe una entrada de /etc/fstab.
type Mount struct {
	// Spec es lo que va en la primera columna: "UUID=..." o "PARTUUID=...".
	Spec string
	// Dir es el punto de montaje.
	Dir string
	// Type es el sistema de ficheros: ext4, vfat, swap...
	Type string
	// Options sin espacios.
	Options string
	// Dump y Pass son las dos ultimas columnas.
	Dump int
	Pass int
}

// Fstab genera el contenido de /etc/fstab.
//
// La primera entrada debe ser la raiz: el generador de systemd la usa para saber
// que el sistema esta completo antes de llegar al objetivo multi-user.
func Fstab(mounts []Mount) (string, error) {
	if len(mounts) == 0 {
		return "", fmt.Errorf("fstab sin entradas")
	}
	if mounts[0].Dir != "/" {
		return "", fmt.Errorf("la primera entrada de fstab tiene que ser la raiz, no %q", mounts[0].Dir)
	}

	var b strings.Builder
	b.WriteString("# /etc/fstab generado por DistroGo. No editar a mano.\n")
	b.WriteString("# <spec> <punto de montaje> <tipo> <opciones> <dump> <pass>\n")

	for _, m := range mounts {
		if m.Spec == "" || m.Dir == "" || m.Type == "" {
			return "", fmt.Errorf("entrada de fstab incompleta: %+v", m)
		}
		opts := m.Options
		if opts == "" {
			opts = "defaults"
		}
		if strings.ContainsAny(opts, " \t") {
			return "", fmt.Errorf("las opciones de %q no pueden tener espacios: %q", m.Dir, opts)
		}

		fmt.Fprintf(&b, "%-22s %-12s %-6s %-18s %d %d\n",
			m.Spec, m.Dir, m.Type, opts, m.Dump, m.Pass)
	}
	return b.String(), nil
}

// UUIDSpec construye la primera columna a partir de un UUID de sistema de ficheros.
func UUIDSpec(uuid string) string {
	return "UUID=" + strings.TrimSpace(uuid)
}

// PARTUUIDSpec construye la primera columna a partir del GUID de una particion.
func PARTUUIDSpec(guid string) string {
	return "PARTUUID=" + strings.TrimSpace(guid)
}
