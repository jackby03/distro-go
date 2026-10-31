package linuxiso

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kdomanski/iso9660"
)

var (
	// ErrNotFound indica que la ruta no existe dentro del ISO.
	ErrNotFound = errors.New("ruta inexistente en el ISO")
	// ErrIsDir indica que se pidio leer un directorio como si fuera un fichero.
	ErrIsDir = errors.New("la ruta es un directorio")
)

// Entry es una entrada del arbol del ISO.
type Entry struct {
	Path  string
	Size  int64
	IsDir bool
}

// ISO es una imagen abierta.
type ISO struct {
	path string
	file *os.File
	img  *iso9660.Image
}

// Open abre un ISO en modo lectura.
func Open(path string) (*ISO, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("abrir el ISO %q: %w", path, err)
	}
	img, err := iso9660.OpenImage(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("leer el indice ISO9660 de %q: %w", path, err)
	}
	return &ISO{path: path, file: f, img: img}, nil
}

// Close cierra la imagen.
func (i *ISO) Close() error {
	if i.file == nil {
		return nil
	}
	if err := i.file.Close(); err != nil {
		return fmt.Errorf("cerrar %q: %w", i.path, err)
	}
	i.file = nil
	return nil
}

// Path es la ruta del ISO abierto.
func (i *ISO) Path() string { return i.path }

// Size es el tamano del ISO en bytes.
func (i *ISO) Size() int64 {
	st, err := i.file.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

// Label es la etiqueta del volumen.
func (i *ISO) Label() (string, error) {
	label, err := i.img.Label()
	if err != nil {
		return "", fmt.Errorf("leer la etiqueta de %q: %w", i.path, err)
	}
	return strings.TrimSpace(label), nil
}

// lookup resuelve una ruta componente a componente, sin distinguir mayusculas.
// La libreria no ofrece busqueda por ruta completa.
func (i *ISO) lookup(name string) (*iso9660.File, error) {
	cur, err := i.img.RootDir()
	if err != nil {
		return nil, fmt.Errorf("leer el directorio raiz de %q: %w", i.path, err)
	}

	clean := strings.Trim(strings.ReplaceAll(name, `\`, "/"), "/")
	if clean == "" {
		return cur, nil
	}

	for _, comp := range strings.Split(clean, "/") {
		kids, err := cur.GetChildren()
		if err != nil {
			return nil, fmt.Errorf("listar %q: %w", cur.Name(), err)
		}
		var next *iso9660.File
		for _, k := range kids {
			if strings.EqualFold(k.Name(), comp) {
				next = k
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		cur = next
	}
	return cur, nil
}

// Exists indica si la ruta existe.
func (i *ISO) Exists(name string) bool {
	_, err := i.lookup(name)
	return err == nil
}

// SizeOf devuelve el tamano de un fichero del ISO.
func (i *ISO) SizeOf(name string) (int64, error) {
	f, err := i.lookup(name)
	if err != nil {
		return 0, err
	}
	if f.IsDir() {
		return 0, fmt.Errorf("%w: %s", ErrIsDir, name)
	}
	return f.Size(), nil
}

// ReaderAt devuelve un lector aleatorio del fichero y su tamano.
//
// Es lo que permite leer el squashfs directamente desde el ISO, en su posicion,
// sin copiarlo a un fichero temporal.
func (i *ISO) ReaderAt(name string) (io.ReaderAt, int64, error) {
	f, err := i.lookup(name)
	if err != nil {
		return nil, 0, err
	}
	if f.IsDir() {
		return nil, 0, fmt.Errorf("%w: %s", ErrIsDir, name)
	}
	r := f.Reader()
	ra, ok := r.(io.ReaderAt)
	if !ok {
		return nil, 0, fmt.Errorf("el lector de %q no admite lectura aleatoria", name)
	}
	return ra, f.Size(), nil
}

// OpenFile abre un fichero del ISO como flujo de solo lectura.
func (i *ISO) OpenFile(name string) (io.ReadCloser, int64, error) {
	ra, size, err := i.ReaderAt(name)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(io.NewSectionReader(ra, 0, size)), size, nil
}

// Walk recorre el arbol a partir de una ruta. El prefijo se entrega como se
// escriba: Walk("EFI") devuelve rutas que empiezan por "EFI/".
func (i *ISO) Walk(prefix string, fn func(Entry) error) error {
	start, err := i.lookup(prefix)
	if err != nil {
		return err
	}
	base := strings.Trim(strings.ReplaceAll(prefix, `\`, "/"), "/")
	if !start.IsDir() {
		return fn(Entry{Path: base, Size: start.Size()})
	}
	return i.walkDir(start, base, fn)
}

func (i *ISO) walkDir(dir *iso9660.File, prefix string, fn func(Entry) error) error {
	kids, err := dir.GetChildren()
	if err != nil {
		return fmt.Errorf("listar %q: %w", prefix, err)
	}
	for _, k := range kids {
		name := prefix + "/" + k.Name()
		if prefix == "" {
			name = k.Name()
		}
		if k.IsDir() {
			if err := fn(Entry{Path: name, IsDir: true}); err != nil {
				return err
			}
			if err := i.walkDir(k, name, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(Entry{Path: name, Size: k.Size()}); err != nil {
			return err
		}
	}
	return nil
}
