// Package rootfs define el contrato entre un árbol de ficheros POSIX de origen
// (un SquashFS, un tar, un directorio) y un filesystem de destino (ext4, FAT32,
// un directorio de pruebas, etc.).
//
// Reglas del contrato:
//   - Path NUNCA lleva "/" inicial y usa "/" como separador.
//   - Walk entrega padres antes que hijos (pre-orden) y nunca "." ni "..".
//   - Open sólo se llama para TypeRegular y el lector debe consumirse hasta EOF
//     antes de pedir el siguiente (o cerrarse explícitamente).
//   - Chmod/Chown se aplican DESPUÉS de escribir el contenido (si no, el setuid
//     se pierde al truncar). SetTimes se aplica al final del árbol.
package rootfs

import (
	"io"
	"io/fs"
	"time"
)

// Type es el tipo de inodo POSIX.
type Type uint8

const (
	TypeDir Type = iota
	TypeRegular
	TypeSymlink
	TypeDevice // block o char; ver Rdev
	TypeFifo
	TypeSocket
)

func (t Type) String() string {
	switch t {
	case TypeDir:
		return "dir"
	case TypeRegular:
		return "regular"
	case TypeSymlink:
		return "symlink"
	case TypeDevice:
		return "device"
	case TypeFifo:
		return "fifo"
	case TypeSocket:
		return "socket"
	default:
		return "unknown"
	}
}

// Entry son los metadatos completos de una entrada.
type Entry struct {
	Path    string      // relativo, sin "/" inicial, separador "/"
	Type    Type        //
	Mode    fs.FileMode // permisos + ModeSetuid/ModeSetgid/ModeSticky
	UID     uint32      //
	GID     uint32      //
	ModTime time.Time   //
	Size    int64       // sólo TypeRegular
	Link    string      // target si TypeSymlink
	Inode   uint64      // nº de inodo origen: identifica hardlinks
	Nlink   uint32      // >1 sólo en ficheros enlazados
	Rdev    uint64      // sólo TypeDevice: (major << 32) | minor
}

func (e Entry) Major() uint32 { return uint32(e.Rdev >> 32) }
func (e Entry) Minor() uint32 { return uint32(e.Rdev) }

// Source es un árbol de ficheros POSIX de origen.
type Source interface {
	Walk(fn func(Entry) error) error
	Open(path string) (io.ReadCloser, error)
}

// Target es un filesystem de destino capaz de materializar el árbol.
//
// Los métodos devuelven el error tal cual: el orquestador decide si un
// ErrNotImplemented es fatal (install real) o tolerable (spike/benchmark).
type Target interface {
	Mkdir(e Entry) error
	Create(e Entry) (io.WriteCloser, error)
	Symlink(e Entry) error
	Hardlink(e Entry) error // e.Path = nuevo enlace, e.Link = ruta ya escrita
	Mknod(e Entry) error
	Chmod(e Entry) error
	Chown(e Entry) error
	SetTimes(e Entry) error
}
