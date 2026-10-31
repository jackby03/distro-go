package ext4fs

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/ext4"

	"distrogo/pkg/rootfs"
)

// Target devuelve el volumen como destino del contrato rootfs.Target.
//
// Se devuelve un adaptador en lugar de implementar el contrato sobre FS para no
// chocar con los metodos simples (Mkdir recibe una ruta, no una entrada) y para
// dejar claro que FS es la capa de volumen y esto solo la traduccion.
func (f *FS) Target() rootfs.Target {
	return &target{fs: f}
}

type target struct {
	fs *FS
}

// filesystem devuelve el volumen, o un error si ya se cerro. Sin esta guarda,
// usar el destino despues de Close seria un puntero nulo.
func (t *target) filesystem() (*ext4.FileSystem, error) {
	if t.fs == nil || t.fs.efs == nil {
		return nil, errors.New("el volumen esta cerrado")
	}
	return t.fs.efs, nil
}

func (t *target) Mkdir(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("crear el directorio", e.Path, efs.Mkdir(e.Path))
}

func (t *target) Create(e rootfs.Entry) (io.WriteCloser, error) {
	efs, err := t.filesystem()
	if err != nil {
		return nil, err
	}
	file, err := efs.OpenFile(e.Path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, mapErr("crear el fichero", e.Path, err)
	}
	return file, nil
}

func (t *target) Symlink(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("crear el symlink", e.Path, efs.Symlink(e.Link, e.Path))
}

func (t *target) Hardlink(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("crear el hardlink", e.Path, efs.Link(e.Link, e.Path))
}

func (t *target) Mknod(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("crear el nodo especial", e.Path,
		efs.Mknod(e.Path, uint32(mknodMode(e)), int(e.Rdev&0xFFFFFFFF)))
}

func (t *target) Chmod(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("cambiar permisos de", e.Path, efs.Chmod(e.Path, e.Mode))
}

func (t *target) Chown(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("cambiar el dueno de", e.Path, efs.Chown(e.Path, int(e.UID), int(e.GID)))
}

func (t *target) SetTimes(e rootfs.Entry) error {
	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("ajustar los tiempos de", e.Path,
		efs.Chtimes(e.Path, e.ModTime, e.ModTime, e.ModTime))
}

// mapErr traduce el "no implementado" de go-diskfs al sentinel del contrato, que
// es lo que permite a Populate decidir si le da igual o si aborta.
func mapErr(what, path string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, filesystem.ErrNotImplemented), errors.Is(err, filesystem.ErrNotSupported):
		return fmt.Errorf("%s %q: %w", what, path, rootfs.ErrUnsupported)
	default:
		return fmt.Errorf("%s %q: %w", what, path, err)
	}
}

// mknodMode construye el "mode" estilo Linux que espera mknod(2).
func mknodMode(e rootfs.Entry) int {
	const (
		sIFIFO  = 0o010000
		sIFCHR  = 0o020000
		sIFBLK  = 0o060000
		sIFSOCK = 0o140000
	)

	m := int(e.Mode.Perm())
	switch e.Type {
	case rootfs.TypeDevice:
		if e.Mode&os.ModeCharDevice != 0 {
			m |= sIFCHR
		} else {
			m |= sIFBLK
		}
	case rootfs.TypeFifo:
		m |= sIFIFO
	case rootfs.TypeSocket:
		m |= sIFSOCK
	}
	return m
}
