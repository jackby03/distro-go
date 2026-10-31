//go:build windows

package blockdev

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// InUse indica si otro proceso tiene el fichero abierto de forma incompatible.
//
// Se comprueba pidiendo acceso de escritura con modo de comparticion cero: si
// otro proceso lo tiene abierto sin compartir, Windows devuelve
// ERROR_SHARING_VIOLATION. Es lo que hace un hipervisor con la imagen de un
// invitado, y escribir por debajo corrompe el disco.
func InUse(path string) (bool, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("convertir %q: %w", path, err)
	}

	h, err := windows.CreateFile(p,
		windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	switch {
	case err == nil:
		_ = windows.CloseHandle(h)
		return false, nil
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION),
		errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return true, nil
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND),
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return false, nil
	default:
		return false, fmt.Errorf("comprobar %q: %w", path, err)
	}
}
