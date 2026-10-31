package windisk

import (
	"fmt"

	"distrogo/pkg/blockdev"
)

// Inventory es el resultado de enumerar los discos de la maquina.
//
// Warnings recoge lo que no se pudo leer. Un inventario con avisos sigue siendo
// utilizable, pero quien vaya a escribir en un disco debe revisarlos: un dato
// ausente no es un dato seguro.
type Inventory struct {
	Disks    []blockdev.Disk
	Warnings []error
}

// Disk devuelve el disco con ese numero.
func (inv Inventory) Disk(number uint32) (blockdev.Disk, bool) {
	for _, d := range inv.Disks {
		if d.Number == number {
			return d, true
		}
	}
	return blockdev.Disk{}, false
}

// warn anade un aviso al inventario.
func (inv *Inventory) warn(format string, args ...any) {
	inv.Warnings = append(inv.Warnings, fmt.Errorf(format, args...))
}
