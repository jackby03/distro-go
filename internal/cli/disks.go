package cli

import (
	"strconv"
	"strings"

	"distrogo/pkg/blockdev"
)

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

	numeros := make([]string, len(disks))
	for i, d := range disks {
		numeros[i] = strconv.FormatUint(uint64(d.Number), 10)
	}
	return strings.Join(numeros, ", ")
}

// estadoIOCTL traduce el resultado de un IOCTL a algo legible.
func estadoIOCTL(err error) string {
	if err == nil {
		return "ok"
	}
	return "FALLO: " + err.Error()
}
