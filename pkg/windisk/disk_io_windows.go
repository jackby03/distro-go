//go:build windows

package windisk

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"

	"distrogo/pkg/blockdev"
)

// ReadAt lee del disco en el byte off.
//
// Windows exige que los accesos a un disco crudo esten alineados al sector, tanto
// el offset como el tamano. Cuando no lo estan se lee el sector completo que
// contiene el dato y se recorta, en lugar de fallar: go-diskfs trabaja con
// bloques de 4096 pero tambien escribe cosas sueltas, como la semilla de checksums.
func (d *Disk) ReadAt(p []byte, off int64) (int, error) {
	if d.handle == windows.InvalidHandle {
		return 0, errors.New("el disco esta cerrado")
	}
	if off < 0 || off+int64(len(p)) > d.size {
		return 0, blockdev.ErrOutOfRange
	}

	if d.alineado(off, len(p)) {
		return d.leerCrudo(p, off)
	}

	desde := off - off%int64(d.sector)
	hasta := d.redondear(off + int64(len(p)))
	buf := make([]byte, hasta-desde)
	if _, err := d.leerCrudo(buf, desde); err != nil {
		return 0, err
	}
	copiados := copy(p, buf[off-desde:])
	return copiados, nil
}

// WriteAt escribe en el disco en el byte off.
//
// Esto es lo que se lleva por delante lo que hubiera. No hay confirmacion posible
// con un bloque de 512 bytes: la decision de llamar aqui la toma la capa de
// arriba, con el serial del disco ya verificado.
//
// Igual que en la lectura, un acceso no alineado se convierte en leer los sectores
// afectados, cambiar la parte pedida y escribirlos enteros. Sin esto, el formateo
// del ext4 falla con ERROR_INVALID_PARAMETER al escribir el GDT.
func (d *Disk) WriteAt(p []byte, off int64) (int, error) {
	if d.handle == windows.InvalidHandle {
		return 0, errors.New("el disco esta cerrado")
	}
	if off < 0 || off+int64(len(p)) > d.size {
		return 0, blockdev.ErrOutOfRange
	}

	if d.alineado(off, len(p)) {
		return d.escribirCrudo(p, off)
	}

	desde := off - off%int64(d.sector)
	hasta := d.redondear(off + int64(len(p)))
	buf := make([]byte, hasta-desde)
	if _, err := d.leerCrudo(buf, desde); err != nil {
		return 0, err
	}
	copy(buf[off-desde:], p)
	if _, err := d.escribirCrudo(buf, desde); err != nil {
		return 0, err
	}
	return len(p), nil
}

// alineado dice si el acceso cumple la alineacion que exige Windows.
func (d *Disk) alineado(off int64, n int) bool {
	s := int64(d.sector)
	return s > 0 && off%s == 0 && int64(n)%s == 0
}

// redondear sube al siguiente multiplo de sector, sin pasarse del disco.
func (d *Disk) redondear(off int64) int64 {
	s := int64(d.sector)
	if s <= 0 {
		return off
	}
	r := ((off + s - 1) / s) * s
	if r > d.size {
		return d.size
	}
	return r
}

// leerCrudo lee sin comprobar alineacion.
func (d *Disk) leerCrudo(p []byte, off int64) (int, error) {
	var done uint32
	ov := &windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(uint64(off) >> 32)}
	if err := windows.ReadFile(d.handle, p, &done, ov); err != nil && !errors.Is(err, windows.ERROR_HANDLE_EOF) {
		return int(done), fmt.Errorf("leer %d bytes en %d: %w", len(p), off, err)
	}
	return int(done), nil
}

// escribirCrudo escribe sin comprobar alineacion.
func (d *Disk) escribirCrudo(p []byte, off int64) (int, error) {
	var done uint32
	ov := &windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(uint64(off) >> 32)}
	if err := windows.WriteFile(d.handle, p, &done, ov); err != nil {
		return int(done), fmt.Errorf("escribir %d bytes en %d: %w", len(p), off, err)
	}
	return int(done), nil
}

// Sync fuerza el volcado a disco. Sin esto, un "listo" puede ser mentira: los
// datos estarian en la cache de Windows y el usuario desconectaria el disco.
func (d *Disk) Sync() error {
	if d.handle == windows.InvalidHandle {
		return nil
	}
	if err := windows.FlushFileBuffers(d.handle); err != nil {
		return fmt.Errorf("volcar la cache del disco %d: %w", d.number, err)
	}
	return nil
}
