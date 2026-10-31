package ext4fs

import (
	"fmt"
	"io"
	"os"

	"github.com/diskfs/go-diskfs/filesystem/ext4"

	"distrogo/pkg/rootfs"
)

// Source presenta el contenido de un directorio del volumen como un arbol de
// rootfs.Source.
//
// Existe para reutilizar Populate dentro del propio destino: copiar /etc/skel a
// /home/<usuario> sin volver a escribir el manejo de permisos, dueno y tiempos,
// que es donde estan los errores caros (los bits setuid).
//
// Las rutas que entrega son relativas al directorio pedido y sin barra inicial,
// como manda el contrato. El directorio en si no se entrega: Rebase y Populate
// trabajan con lo que cuelga de el.
func Source(f *FS, dir string) rootfs.Source {
	return &volumeSource{fs: f, dir: cleanPath(dir)}
}

type volumeSource struct {
	fs  *FS
	dir string
}

func (s *volumeSource) Walk(fn func(rootfs.Entry) error) error {
	if s.fs.efs == nil {
		return fmt.Errorf("el volumen esta cerrado")
	}
	return s.walk(s.dir, "", fn)
}

// walk recorre un directorio de go-diskfs entregando rutas relativas a la raiz
// del Source, padres antes que hijos.
func (s *volumeSource) walk(dir, prefix string, fn func(rootfs.Entry) error) error {
	infos, err := s.fs.efs.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listar %q: %w", dir, err)
	}

	for _, de := range infos {
		name := de.Name()

		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}

		full := name
		if dir != "." {
			full = dir + "/" + name
		}

		// DirEntry solo trae el tipo y ReadDir pierde los permisos (go-diskfs
		// construye ese FileInfo con el tipo a secas), asi que el modo bueno sale
		// de Stat. Un stat por entrada es el precio de no copiar permisos a cero.
		fi, err := s.fs.efs.Stat(full)
		if err != nil {
			return fmt.Errorf("stat de %q: %w", full, err)
		}

		e := entryFromFileInfo(fi, rel)
		if e.Type == rootfs.TypeSymlink && e.Link == "" {
			// StatT suele traer el destino, pero no siempre en inodos antiguos.
			if target, err := s.fs.efs.ReadLink(full); err == nil {
				e.Link = target
			}
		}

		if err := fn(e); err != nil {
			return err
		}
		if e.Type == rootfs.TypeDir {
			if err := s.walk(full, rel, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *volumeSource) Open(path string) (io.ReadCloser, error) {
	full := cleanPath(path)
	if s.dir != "." {
		full = s.dir + "/" + full
	}
	rc, _, err := s.fs.OpenFile(full)
	return rc, err
}

// entryFromFileInfo traduce lo que da go-diskfs al contrato de rootfs.
func entryFromFileInfo(fi os.FileInfo, rel string) rootfs.Entry {
	mode := fi.Mode()

	e := rootfs.Entry{
		Path:    rel,
		Type:    typeOf(mode),
		Mode:    mode,
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
	}
	if e.Type != rootfs.TypeRegular {
		e.Size = 0
	}

	// Sys trae lo que os.FileInfo no puede: dueno, inodo y dispositivo.
	info, ok := fi.(*ext4.FileInfo)
	if !ok {
		return e
	}
	st, ok := info.Sys().(*ext4.StatT)
	if !ok {
		return e
	}

	e.UID = st.UID
	e.GID = st.GID
	e.Inode = uint64(st.Ino)
	e.Nlink = uint32(st.Nlink)
	e.Rdev = (uint64(st.Major) << 32) | uint64(st.Minor)
	e.Link = st.LinkTarget

	return e
}

// typeOf traduce los bits de modo a un tipo de inodo POSIX.
func typeOf(mode os.FileMode) rootfs.Type {
	switch {
	case mode&os.ModeSymlink != 0:
		return rootfs.TypeSymlink
	case mode&os.ModeDevice != 0:
		return rootfs.TypeDevice
	case mode&os.ModeNamedPipe != 0:
		return rootfs.TypeFifo
	case mode&os.ModeSocket != 0:
		return rootfs.TypeSocket
	case mode.IsDir():
		return rootfs.TypeDir
	default:
		return rootfs.TypeRegular
	}
}
