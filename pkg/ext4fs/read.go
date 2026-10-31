package ext4fs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"

	"distrogo/pkg/blockdev"
)

// Entry es una entrada del arbol.
type Entry struct {
	Name  string
	Size  int64
	IsDir bool
}

// Stat devuelve una entrada del volumen.
func (f *FS) Stat(path string) (Entry, error) {
	if f.efs == nil {
		return Entry{}, fmt.Errorf("el volumen esta cerrado")
	}
	fi, err := f.efs.Stat(cleanPath(path))
	if err != nil {
		return Entry{}, fmt.Errorf("stat de %q: %w", path, err)
	}
	return Entry{Name: fi.Name(), Size: fi.Size(), IsDir: fi.IsDir()}, nil
}

// List devuelve el contenido de un directorio.
func (f *FS) List(path string) ([]Entry, error) {
	if f.efs == nil {
		return nil, fmt.Errorf("el volumen esta cerrado")
	}

	entries, err := f.efs.ReadDir(cleanPath(path))
	if err != nil {
		return nil, fmt.Errorf("listar %q: %w", path, err)
	}

	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{Name: e.Name(), IsDir: e.IsDir()})
	}
	return out, nil
}

// OpenFile abre un fichero para leerlo como flujo, con su tamano.
func (f *FS) OpenFile(path string) (io.ReadCloser, int64, error) {
	if f.efs == nil {
		return nil, 0, fmt.Errorf("el volumen esta cerrado")
	}

	p := cleanPath(path)
	fi, err := f.efs.Stat(p)
	if err != nil {
		return nil, 0, fmt.Errorf("stat de %q: %w", path, err)
	}
	if fi.IsDir() {
		return nil, 0, fmt.Errorf("%q es un directorio", path)
	}

	file, err := f.efs.OpenFile(p, os.O_RDONLY)
	if err != nil {
		return nil, 0, fmt.Errorf("abrir %q: %w", path, err)
	}
	return file, fi.Size(), nil
}

// CopyFile copia un fichero del volumen a w con buffer acotado.
func (f *FS) CopyFile(path string, w io.Writer) (int64, error) {
	rc, _, err := f.OpenFile(path)
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	n, err := io.Copy(w, rc)
	if err != nil {
		return n, fmt.Errorf("copiar %q: %w", path, err)
	}
	return n, nil
}

// cleanPath quita la barra inicial: go-diskfs trabaja con rutas relativas.
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

// Remove borra un fichero o un directorio vacio.
//
// Funciona sola, pero no se puede alternar con escrituras en el mismo
// directorio: go-diskfs reutiliza inodos de entradas vivas y acaba dando a un
// fichero el contenido de otro. Para reescribir existe OverwriteFile, y para
// crecer, AppendFile.
func (f *FS) Remove(path string) error {
	if f.efs == nil {
		return fmt.Errorf("el volumen esta cerrado")
	}
	if err := f.efs.Remove(cleanPath(path)); err != nil {
		return fmt.Errorf("borrar %q: %w", path, err)
	}
	return nil
}

// AppendFile anade contenido al final de un fichero que ya existe.
//
// Es la forma segura de crecer un fichero: no toca su inodo ni su entrada de
// directorio. Los ficheros de cuentas solo necesitan lineas nuevas, asi que no
// hay motivo para reescribirlos.
func (f *FS) AppendFile(path string, content []byte) error {
	if f.efs == nil {
		return fmt.Errorf("el volumen esta cerrado")
	}

	file, err := f.efs.OpenFile(cleanPath(path), os.O_RDWR)
	if err != nil {
		return fmt.Errorf("abrir %q para escribir: %w", path, err)
	}

	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = file.Close()
		return fmt.Errorf("ir al final de %q: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("escribir %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cerrar %q: %w", path, err)
	}
	return nil
}

// OverwriteFile reescribe el contenido de un fichero sin borrarlo.
//
// Hace falta porque go-diskfs no trunca y su Remove no es fiable: alternar
// Remove y escritura en el mismo directorio reutiliza inodos de entradas vivas
// y acaba dando a un fichero el contenido de otro. Aqui se escribe encima del
// mismo inodo, y si el contenido nuevo es mas corto se rellena con saltos de
// linea, que no significan nada en los ficheros que se reescriben (fstab,
// passwd, group, shadow).
func (f *FS) OverwriteFile(path string, content []byte) error {
	if f.efs == nil {
		return fmt.Errorf("el volumen esta cerrado")
	}

	info, err := f.Stat(path)
	if err != nil {
		return f.WriteFile(path, bytes.NewReader(content))
	}

	file, err := f.efs.OpenFile(cleanPath(path), os.O_RDWR)
	if err != nil {
		return fmt.Errorf("abrir %q para escribir: %w", path, err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return fmt.Errorf("rebobinar %q: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("escribir %q: %w", path, err)
	}

	if falta := info.Size - int64(len(content)); falta > 0 {
		relleno := bytes.Repeat([]byte{'\n'}, int(falta))
		if _, err := file.Write(relleno); err != nil {
			_ = file.Close()
			return fmt.Errorf("rellenar %q: %w", path, err)
		}
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("cerrar %q: %w", path, err)
	}
	return nil
}

// ensureChecksumSeed repara una incompatibilidad de go-diskfs con e2fsck.
//
// Al formatear, go-diskfs guarda en s_checksum_seed (offset 0x270 del
// superbloque) la semilla de la que dependen los checksums de inodos y bloques de
// directorio. Pero ese campo solo tiene validez si el sistema de ficheros usa la
// feature metadata_csum_seed; sin ella, e2fsck lo pone a cero al reescribir el
// superbloque, y entonces go-diskfs ya no puede leer sus propios inodos
// ("checksum mismatch").
//
// La semilla que espera es crc32c(0xffffffff, uuid). Se rellena si esta a cero,
// lo que no afecta a Linux: sin la feature, el campo se ignora.
func ensureChecksumSeed(dev blockdev.Writable, offset int64) error {
	buf := make([]byte, 1024)
	if _, err := dev.ReadAt(buf, offset+1024); err != nil {
		return fmt.Errorf("leer el superbloque: %w", err)
	}
	if binary.LittleEndian.Uint16(buf[0x38:]) != 0xEF53 {
		return ErrNotExt4
	}
	if binary.LittleEndian.Uint32(buf[0x270:]) != 0 {
		return nil
	}

	seed := crc32.Update(0xffffffff, crc32.MakeTable(crc32.Castagnoli), buf[0x68:0x78])
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], seed)
	if _, err := dev.WriteAt(raw[:], offset+1024+0x270); err != nil {
		return fmt.Errorf("escribir la semilla de checksums: %w", err)
	}
	return nil
}
