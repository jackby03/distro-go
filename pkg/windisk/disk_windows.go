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

// Close cierra las manejadoras: primero el disco y despues los volumenes, cuyo
// cierre es lo que libera el bloqueo.
func (d *Disk) Close() error {
	var primero error

	// Los que se sacaron de linea vuelven a estarlo antes de soltar el handle:
	// cerrado el handle, el IOCTL ya no llega a ninguna parte. Y el error no se
	// descarta, porque un volumen que se queda fuera de linea desaparece del
	// Explorador hasta que se reconecta el disco.
	for _, h := range d.offline {
		if err := ioctlSimple(h, ioctlVolumeOnline); err != nil && primero == nil {
			primero = fmt.Errorf("devolver a linea el volumen: %w", err)
		}
	}
	d.offline = nil
	d.offlinePaths = nil

	for _, h := range d.volumes {
		if err := windows.CloseHandle(h); err != nil && primero == nil {
			primero = fmt.Errorf("cerrar el volumen: %w", err)
		}
	}
	d.volumes = nil

	if d.handle != windows.InvalidHandle {
		if err := windows.CloseHandle(d.handle); err != nil && primero == nil {
			primero = fmt.Errorf("cerrar el disco %d: %w", d.number, err)
		}
		d.handle = windows.InvalidHandle
	}

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
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return windows.InvalidHandle, fmt.Errorf("abrir %q en exclusiva: algun volumen sigue en uso (uno sin letra de unidad, o un programa con el disco abierto): %w", path, err)
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
