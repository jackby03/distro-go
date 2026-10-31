package rootfs

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrUnsupported indica que el destino no sabe representar esa entrada. Sirve
// para que el que llama decida si le da igual (un ESP no tiene /dev) o si es un
// fallo (un rootfs sin sus ficheros).
var ErrUnsupported = errors.New("el destino no soporta esta operacion")

// PopulateOptions ajusta el volcado.
type PopulateOptions struct {
	// Buffer es el tamano del buffer de copia. Cero usa 1 MiB. Nunca se carga un
	// fichero entero en memoria.
	Buffer int
	// TolerateUnsupported sigue adelante cuando el destino devuelve
	// ErrUnsupported, contandolo en Stats.Unsupported.
	TolerateUnsupported bool
	// Progress, si no es nil, se llama periodicamente con lo que lleva y la
	// entrada que esta procesando. Sirve para no dejar al usuario veinte minutos
	// mirando una pantalla muda sin saber si avanza.
	Progress func(stats Stats, current string)
}

// Stats resume lo que se ha volcado.
type Stats struct {
	Dirs        int64
	Files       int64
	Symlinks    int64
	Hardlinks   int64
	Devices     int64
	Unsupported int64
	Bytes       int64
}

// Populate copia un arbol de Source a Target.
//
// Respeta el orden del contrato: crea cada entrada, escribe su contenido y solo
// despues ajusta permisos y dueno. Los tiempos de los directorios se aplican al
// final y de hijo a padre, porque crear hijos cambia el mtime del padre.
func Populate(src Source, tgt Target, opts PopulateOptions) (Stats, error) {
	if src == nil || tgt == nil {
		return Stats{}, errors.New("volcar: hacen falta origen y destino")
	}

	bufSize := opts.Buffer
	if bufSize <= 0 {
		bufSize = 1 << 20
	}
	buf := make([]byte, bufSize)

	var stats Stats
	lastReport := time.Now()
	var dirs []Entry
	firstPath := make(map[uint64]string)

	// handle aplica la politica: un error se propaga, salvo que el destino no
	// soporte la operacion y se haya pedido tolerarlo.
	handle := func(err error, what string, e Entry) (bool, error) {
		switch {
		case err == nil:
			return true, nil
		case opts.TolerateUnsupported && errors.Is(err, ErrUnsupported):
			stats.Unsupported++
			return false, nil
		default:
			return false, fmt.Errorf("%s %q: %w", what, e.Path, err)
		}
	}

	err := src.Walk(func(e Entry) error {
		if opts.Progress != nil && time.Since(lastReport) >= 500*time.Millisecond {
			opts.Progress(stats, e.Path)
			lastReport = time.Now()
		}

		switch e.Type {
		case TypeDir:
			if _, err := handle(tgt.Mkdir(e), "crear el directorio", e); err != nil {
				return err
			}
			if _, err := handle(tgt.Chmod(e), "cambiar permisos de", e); err != nil {
				return err
			}
			if _, err := handle(tgt.Chown(e), "cambiar el dueno de", e); err != nil {
				return err
			}
			dirs = append(dirs, e)
			stats.Dirs++

		case TypeRegular:
			// Un hardlink se escribe una vez y las demas rutas apuntan a la
			// primera. Si el destino no sabe enlazar, se copia el contenido: es
			// peor en espacio, pero el fichero existe, que es lo que importa.
			if e.Nlink > 1 {
				if first, ok := firstPath[e.Inode]; ok {
					linked, err := handle(tgt.Hardlink(Entry{Path: e.Path, Link: first}), "enlazar", e)
					if err != nil {
						return err
					}
					if linked {
						stats.Hardlinks++
						return nil
					}
				} else {
					firstPath[e.Inode] = e.Path
				}
			}

			w, err := tgt.Create(e)
			if err != nil {
				return fmt.Errorf("crear %q: %w", e.Path, err)
			}
			rc, err := src.Open(e.Path)
			if err != nil {
				_ = w.Close()
				return fmt.Errorf("abrir %q: %w", e.Path, err)
			}

			n, copyErr := io.CopyBuffer(w, rc, buf)
			_ = rc.Close()
			if closeErr := w.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return fmt.Errorf("copiar %q: %w", e.Path, copyErr)
			}

			stats.Files++
			stats.Bytes += n

			if _, err := handle(tgt.Chmod(e), "cambiar permisos de", e); err != nil {
				return err
			}
			if _, err := handle(tgt.Chown(e), "cambiar el dueno de", e); err != nil {
				return err
			}

		case TypeSymlink:
			if ok, err := handle(tgt.Symlink(e), "crear el symlink", e); err != nil {
				return err
			} else if ok {
				stats.Symlinks++
			}

		default:
			if ok, err := handle(tgt.Mknod(e), "crear el nodo especial", e); err != nil {
				return err
			} else if ok {
				stats.Devices++
			}
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	// Del mas profundo al mas somero: crear un hijo cambia el mtime del padre.
	for i := len(dirs) - 1; i >= 0; i-- {
		if _, err := handle(tgt.SetTimes(dirs[i]), "ajustar los tiempos de", dirs[i]); err != nil {
			return stats, err
		}
	}

	return stats, nil
}
