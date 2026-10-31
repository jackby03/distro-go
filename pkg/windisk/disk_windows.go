//go:build windows

package windisk

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"distrogo/pkg/blockdev"
)

// Comandos de volumen de winioctl.h. No escriben datos: quitan el volumen del
// sistema de ficheros para que nadie mas lo use.
const (
	fsctlLockVolume     = 0x00090018
	fsctlDismountVolume = 0x00090020

	// IOCTL_VOLUME_OFFLINE deja el volumen fuera de linea. Es la via que usan
	// Rufus y dd: el sistema deja de tocarlo aunque queden handles abiertos, y es
	// la unica forma de seguir cuando un antivirus o el indexador no sueltan la
	// unidad. Se deshace con IOCTL_VOLUME_ONLINE al cerrar.
	ioctlVolumeOffline = 0x0056C000
	ioctlVolumeOnline  = 0x0056C004
)

// intentosBloqueo es cuantas veces se reintenta bloquear un volumen. Windows suele
// soltarlo en el primer intento, pero el Explorador, el indexador o el antivirus
// pueden tenerlo abierto un instante despues del desmontaje.
const intentosBloqueo = 10

// esperaBloqueo es la pausa entre intentos.
const esperaBloqueo = 100 * time.Millisecond

// Disk es un disco fisico abierto en lectura y escritura.
//
// Mientras vive, sus volumenes estan bloqueados y desmontados: nadie mas escribe
// en el, que es lo unico que garantiza que el particionado y el volcado no se
// crucen con otro programa.
type Disk struct {
	number  int
	handle  windows.Handle
	volumes []windows.Handle
	// offline son los volumenes que hubo que sacar de linea. Se devuelven a
	// linea al cerrar, o la unidad desapareceria del Explorador.
	offline      []windows.Handle
	offlinePaths []string
	paths        []string
	size         int64
	sector       int
}

var _ blockdev.Writable = (*Disk)(nil)

// DiskPath devuelve la ruta de dispositivo de un disco fisico.
func DiskPath(number int) string {
	return fmt.Sprintf(`\\.\PhysicalDrive%d`, number)
}

// OpenExclusive abre un disco fisico para lectura y escritura.
//
// El orden importa y es el que documenta Microsoft: primero se bloquea y desmonta
// cada volumen del disco, y solo despues se abre el disco sin compartir con nadie.
// Al reves no funciona: con cualquier volumen montado, abrir el disco en exclusiva
// devuelve ERROR_SHARING_VIOLATION.
//
// Hace falta una consola elevada. El error lo dice con esas palabras si Windows
// responde ERROR_ACCESS_DENIED.
func OpenExclusive(number int) (*Disk, error) {
	if number < 0 {
		return nil, fmt.Errorf("numero de disco invalido: %d", number)
	}

	paths, err := volumesOnDisk(number)
	if err != nil {
		return nil, err
	}

	d := &Disk{number: number, handle: windows.InvalidHandle}
	cerrado := false
	defer func() {
		if !cerrado {
			_ = d.Close()
		}
	}()

	for _, path := range paths {
		h, offline, err := lockAndDismount(path)
		if err != nil {
			return nil, err
		}
		d.volumes = append(d.volumes, h)
		if offline {
			d.offline = append(d.offline, h)
			d.offlinePaths = append(d.offlinePaths, path)
		}
		d.paths = append(d.paths, path)
	}

	handle, err := openDiskExclusive(DiskPath(number))
	if err != nil {
		return nil, err
	}
	d.handle = handle

	size, sector, err := queryGeometry(handle)
	if err != nil {
		return nil, fmt.Errorf("leer la geometria del disco %d: %w", number, err)
	}
	d.size, d.sector = size, sector

	cerrado = true
	return d, nil
}

// Number es el numero de disco fisico.
func (d *Disk) Number() int { return d.number }

// LockedVolumes son las rutas de los volumenes que quedaron bloqueados.
func (d *Disk) LockedVolumes() []string {
	out := make([]string, len(d.paths))
	copy(out, d.paths)
	return out
}

func (d *Disk) Size() int64     { return d.size }
func (d *Disk) SectorSize() int { return d.sector }

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

// Close cierra las manejadoras: primero el disco y despues los volumenes, cuyo
// cierre es lo que libera el bloqueo.
func (d *Disk) Close() error {
	var primero error

	if d.handle != windows.InvalidHandle {
		if err := windows.CloseHandle(d.handle); err != nil {
			primero = fmt.Errorf("cerrar el disco %d: %w", d.number, err)
		}
		d.handle = windows.InvalidHandle
	}

	for _, h := range d.volumes {
		if err := windows.CloseHandle(h); err != nil && primero == nil {
			primero = fmt.Errorf("cerrar el volumen: %w", err)
		}
	}
	// Los que se sacaron de linea vuelven a estarlo antes de soltar el handle, o
	// la unidad se quedaria sin letra hasta reiniciar.
	for _, h := range d.offline {
		_ = ioctlSimple(h, ioctlVolumeOnline)
	}
	d.offline = nil
	d.volumes = nil

	return primero
}

// volumesOnDisk devuelve las rutas de los volumenes que viven en un disco.
//
// Recorre las letras de unidad, como el inventario: es lo que Windows deja ver sin
// elevar. Un volumen del disco sin letra no se bloquea, pero tampoco esta montado,
// asi que no impide abrir el disco en exclusiva.
func volumesOnDisk(number int) ([]string, error) {
	var paths []string

	for letter := byte('A'); letter <= byte('Z'); letter++ {
		h, err := openDevice(fmt.Sprintf(`\\.\%c:`, letter), 0)
		if err != nil {
			continue // unidad inexistente o sin acceso: no es un error
		}

		extents, err := queryDiskExtents(h)
		_ = windows.CloseHandle(h)
		if err != nil {
			continue
		}

		enDisco := false
		for _, e := range extents {
			if int(e.DiskNumber) == number {
				enDisco = true
				break
			}
		}
		if !enDisco {
			continue
		}

		// Se usa la forma \\.\X: y no la del GUID del volumen: en este Windows
		// \\?\Volume{GUID}\ no resuelve (ERROR_PATH_NOT_FOUND) aunque la ruta sea
		// la que informa Get-Volume, y la letra si abre para lectura y escritura.
		paths = append(paths, fmt.Sprintf(`\\.\%c:`, letter))
	}

	return paths, nil
}

// lockAndDismount toma un volumen para poder escribir en el disco.
//
// Prueba los tres caminos en orden, de menos a mas agresivo, porque un antivirus
// puede vetar uno solo de ellos: desmontar, bloquear en exclusiva y sacar el
// volumen de linea. El bloqueo dura lo que dure el handle.
//
// Devuelve ademas si hubo que sacar el volumen de linea, para poder devolverlo a
// linea al cerrar.
func lockAndDismount(path string) (windows.Handle, bool, error) {
	h, err := openVolumeForControl(path)
	if err != nil {
		return windows.InvalidHandle, false, err
	}

	dismountErr := reintentar(func() error { return ioctlSimple(h, fsctlDismountVolume) })
	lockErr := reintentar(func() error { return ioctlSimple(h, fsctlLockVolume) })
	if lockErr == nil {
		return h, false, nil
	}

	if !IsElevated() {
		_ = windows.CloseHandle(h)
		return windows.InvalidHandle, false, fmt.Errorf("bloquear el volumen %q: hace falta una consola elevada (ejecutar como administrador)", path)
	}

	// Con el bloqueo vetado, queda sacar el volumen de linea: es lo que hacen
	// Rufus y dd cuando otro controlador no suelta la unidad.
	offlineErr := reintentar(func() error { return ioctlSimple(h, ioctlVolumeOffline) })
	if offlineErr == nil {
		return h, true, nil
	}

	_ = windows.CloseHandle(h)
	var detalle []string
	if dismountErr != nil {
		detalle = append(detalle, "desmontar: "+dismountErr.Error())
	} else {
		detalle = append(detalle, "desmontar: ok")
	}
	detalle = append(detalle, "bloquear: "+lockErr.Error())
	detalle = append(detalle, "fuera de linea: "+offlineErr.Error())

	return windows.InvalidHandle, false, fmt.Errorf(
		"el volumen %q no se pudo tomar (%s)", path, strings.Join(detalle, "; "))
}

// VolumeProbe es el resultado de probar los IOCTL de un volumen, uno por uno.
//
// Existe porque un antivirus puede vetar uno solo: sin esta tabla es imposible
// saber si el problema son los permisos, el desmontaje o el bloqueo exclusivo, y
// cada uno se resuelve de forma distinta.
type VolumeProbe struct {
	Path     string
	Open     error
	Dismount error
	Lock     error
	Offline  error
	Restore  error
}

// ProbeDisk prueba los IOCTL necesarios para tomar un disco en exclusiva y
// devuelve el error de cada uno. No escribe datos y deja el volumen como estaba:
// si lo saca de linea, lo vuelve a poner en linea.
func ProbeDisk(number int) ([]VolumeProbe, error) {
	paths, err := volumesOnDisk(number)
	if err != nil {
		return nil, err
	}

	probes := make([]VolumeProbe, 0, len(paths))
	for _, path := range paths {
		p := VolumeProbe{Path: path}

		h, err := openVolumeForControl(path)
		if err != nil {
			p.Open = err
			probes = append(probes, p)
			continue
		}

		p.Dismount = reintentar(func() error { return ioctlSimple(h, fsctlDismountVolume) })
		p.Lock = reintentar(func() error { return ioctlSimple(h, fsctlLockVolume) })
		// Fuera de linea solo tiene sentido si el bloqueo fallo: con el volumen ya
		// bloqueado, el IOCTL responde "incorrect function" y solo confunde.
		if p.Lock != nil {
			p.Offline = reintentar(func() error { return ioctlSimple(h, ioctlVolumeOffline) })
			if p.Offline == nil {
				p.Restore = ioctlSimple(h, ioctlVolumeOnline)
			}
		}

		_ = windows.CloseHandle(h)
		probes = append(probes, p)
	}

	return probes, nil
}

// openVolumeForControl abre un volumen para mandarle IOCTL de control.
func openVolumeForControl(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("convertir %q: %w", path, err)
	}

	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return windows.InvalidHandle, fmt.Errorf("abrir el volumen %q: hace falta una consola elevada (ejecutar como administrador): %w", path, err)
		}
		return windows.InvalidHandle, fmt.Errorf("abrir el volumen %q: %w", path, err)
	}
	return h, nil
}

// openDiskExclusive abre el disco fisico sin compartir con nadie.
func openDiskExclusive(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("convertir %q: %w", path, err)
	}

	var handle windows.Handle
	err = reintentar(func() error {
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return err
		}
		handle = h
		return nil
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return windows.InvalidHandle, fmt.Errorf("abrir %q: hace falta una consola elevada (ejecutar como administrador): %w", path, err)
		}
		return windows.InvalidHandle, fmt.Errorf("abrir %q en exclusiva: %w", path, err)
	}
	return handle, nil
}

// ioctlSimple lanza un IOCTL sin buffers de entrada ni de salida.
func ioctlSimple(h windows.Handle, code uint32) error {
	_, err := ioctl(h, code, nil, nil)
	return err
}

// reintentar repite una operacion que puede fallar de forma transitoria porque
// otro proceso tiene el volumen abierto.
func reintentar(fn func() error) error {
	var err error
	for intento := 0; intento < intentosBloqueo; intento++ {
		if err = fn(); err == nil {
			return nil
		}
		// Que falten permisos no se arregla esperando.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		time.Sleep(esperaBloqueo)
	}
	return err
}

// IsElevated dice si el proceso corre con privilegios de administrador.
//
// Hace falta para no mentir en los errores: FSCTL_LOCK_VOLUME devuelve
// ERROR_ACCESS_DENIED tanto si faltan permisos como si otro proceso tiene el
// volumen abierto, y las dos cosas se arreglan de forma distinta.
func IsElevated() bool {
	token := windows.GetCurrentProcessToken()
	return token.IsElevated()
}

// OfflineVolumes son los volumenes que hubo que sacar de linea porque otro
// programa los tenia abiertos. Se dice en voz alta: es una medida mas agresiva que
// bloquear, y quien lo lea merece saberlo.
func (d *Disk) OfflineVolumes() []string {
	out := make([]string, len(d.offlinePaths))
	copy(out, d.offlinePaths)
	return out
}
