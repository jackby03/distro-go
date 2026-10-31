package ext4fs

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	pathpkg "path"

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
	return &target{fs: f, duenos: map[string][2]uint32{".": {0, 0}}}
}

type target struct {
	fs *FS
	// duenos recuerda el dueno con el que quedaron los directorios visitados.
	//
	// Hace falta porque una entrada nueva hereda el dueno de su padre: dentro de
	// /home/ana, que es 1000:1000, un fichero nace 1000:1000 aunque queramos
	// root. Comparar con lo que ya hay evita los Chown inutiles, que son un
	// recorrido completo de la ruta cada uno, sin saltarselos donde importan.
	duenos map[string][2]uint32
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

// modeReciennNacido es el modo con el que go-diskfs crea ficheros y directorios.
//
// Verificado sobre una imagen recien formateada: Create y Mkdir dejan 0755 y
// dueno 0:0. Cambiar eso a lo mismo cuesta un recorrido completo de la ruta por
// cada entrada, y en un rootfs la mayoria son root:root a 0755.
const modeRecienNacido = 0755

func (t *target) Chmod(e rootfs.Entry) error {
	// Nada que hacer si ya nace asi: los bits especiales (setuid, setgid,
	// sticky) no entran en esta comparacion, asi que esos si se aplican.
	if e.Mode == modeRecienNacido && e.Type == rootfs.TypeRegular {
		return nil
	}
	if e.Mode == iofs.ModeDir|modeRecienNacido && e.Type == rootfs.TypeDir {
		return nil
	}

	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	return mapErr("cambiar permisos de", e.Path, efs.Chmod(e.Path, e.Mode))
}

func (t *target) Chown(e rootfs.Entry) error {
	quiere := [2]uint32{e.UID, e.GID}

	// Si el padre ya tiene ese dueno, el hijo nace con el: no hay nada que
	// cambiar. En un rootfs eso cubre casi todo el arbol. Un padre desconocido
	// no vale como 0:0: hay que preguntar, porque puede ser el home de alguien.
	if dueno, conocido := t.duenos[pathpkg.Dir(e.Path)]; conocido && dueno == quiere {
		t.duenos[e.Path] = quiere
		return nil
	}

	efs, err := t.filesystem()
	if err != nil {
		return err
	}
	if err := mapErr("cambiar el dueno de", e.Path, efs.Chown(e.Path, int(e.UID), int(e.GID))); err != nil {
		return err
	}
	t.duenos[e.Path] = quiere
	return nil
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
