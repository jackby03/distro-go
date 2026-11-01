//go:build windows

package windisk

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

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
