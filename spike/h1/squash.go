package main

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/CalebQ42/squashfs"
	squashfslow "github.com/CalebQ42/squashfs/low"
	"github.com/CalebQ42/squashfs/low/directory"
	"github.com/CalebQ42/squashfs/low/inode"

	"distrogo/pkg/rootfs"
)

// squashSource es el adapter de CalebQ42/squashfs al contrato rootfs.Source.
//
// Notas de la evaluación de la librería:
//   - FS.OpenFile usa filepath.Clean, que en Windows convierte "/" en "\" y
//     rompe cualquier ruta con más de un componente. Por eso aquí navegamos
//     componente a componente (nunca pasamos un "/" a la librería).
//   - La API low-level sí expone lo que necesitamos (nº de inodo, nlink, dev,
//     uid/gid vía tabla de IDs y target de symlink), así que el walk se hace
//     con low y sólo el contenido con la API alta.
type squashSource struct {
	r *squashfs.Reader

	// caché de un nivel: el walk es DFS, así que los ficheros de un mismo
	// directorio llegan seguidos y el padre se resuelve una sola vez.
	cachePath string
	cacheFS   squashfs.FS
	hasCache  bool
}

func newSquashSource(r *squashfs.Reader) *squashSource {
	return &squashSource{r: r}
}

func (s *squashSource) Walk(fn func(rootfs.Entry) error) error {
	return s.walkDir(s.r.Low.Root, "", fn)
}

func (s *squashSource) walkDir(d squashfslow.Directory, prefix string, fn func(rootfs.Entry) error) error {
	for _, de := range d.Entries {
		// squashfs almacena "." y ".." como entradas reales.
		if de.Name == "." || de.Name == ".." {
			continue
		}
		base, err := s.r.Low.BaseFromEntry(de)
		if err != nil {
			return fmt.Errorf("%s: leer inodo: %w", path.Join(prefix, de.Name), err)
		}
		e, err := s.entry(base, prefix, de)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
		if e.Type == rootfs.TypeDir {
			sub, err := base.ToDir(s.r.Low)
			if err != nil {
				return fmt.Errorf("%s: leer directorio: %w", e.Path, err)
			}
			if err := s.walkDir(sub, e.Path, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *squashSource) entry(b squashfslow.FileBase, prefix string, de directory.Entry) (rootfs.Entry, error) {
	e := rootfs.Entry{
		Path:    de.Name,
		Inode:   uint64(b.Inode.Num),
		ModTime: time.Unix(int64(b.Inode.ModTime), 0),
		Nlink:   1,
		Mode:    permToMode(b.Inode.Perm),
	}
	if prefix != "" {
		e.Path = prefix + "/" + de.Name
	}

	uid, err := b.Uid(&s.r.Low)
	if err != nil {
		return e, fmt.Errorf("%s: uid: %w", e.Path, err)
	}
	gid, err := b.Gid(&s.r.Low)
	if err != nil {
		return e, fmt.Errorf("%s: gid: %w", e.Path, err)
	}
	e.UID, e.GID = uid, gid

	switch b.Inode.Type {
	case inode.Dir, inode.EDir:
		e.Type = rootfs.TypeDir
		e.Mode |= fs.ModeDir

	case inode.Fil:
		e.Type = rootfs.TypeRegular
		e.Size = int64(b.Inode.Data.(inode.File).Size)
	case inode.EFil:
		f := b.Inode.Data.(inode.EFile)
		e.Type = rootfs.TypeRegular
		e.Size = int64(f.Size)
		e.Nlink = f.LinkCount

	case inode.Sym:
		sym := b.Inode.Data.(inode.Symlink)
		e.Type = rootfs.TypeSymlink
		e.Link = string(sym.Target)
		e.Nlink = sym.LinkCount
		e.Mode |= fs.ModeSymlink
	case inode.ESym:
		sym := b.Inode.Data.(inode.ESymlink)
		e.Type = rootfs.TypeSymlink
		e.Link = string(sym.Target)
		e.Nlink = sym.LinkCount
		e.Mode |= fs.ModeSymlink

	case inode.Block, inode.Char:
		d := b.Inode.Data.(inode.Device)
		e.Type = rootfs.TypeDevice
		e.Rdev = linuxRdev(d.Dev)
		e.Nlink = d.LinkCount
		e.Mode |= fs.ModeDevice
		if b.Inode.Type == inode.Char {
			e.Mode |= fs.ModeCharDevice
		}
	case inode.EBlock, inode.EChar:
		d := b.Inode.Data.(inode.EDevice)
		e.Type = rootfs.TypeDevice
		e.Rdev = linuxRdev(d.Dev)
		e.Nlink = d.LinkCount
		e.Mode |= fs.ModeDevice
		if b.Inode.Type == inode.EChar {
			e.Mode |= fs.ModeCharDevice
		}

	case inode.Fifo, inode.Sock:
		ipc := b.Inode.Data.(inode.IPC)
		e.Nlink = ipc.LinkCount
		if b.Inode.Type == inode.Fifo {
			e.Type = rootfs.TypeFifo
			e.Mode |= fs.ModeNamedPipe
		} else {
			e.Type = rootfs.TypeSocket
			e.Mode |= fs.ModeSocket
		}
	case inode.EFifo, inode.ESock:
		ipc := b.Inode.Data.(inode.EIPC)
		e.Nlink = ipc.LinkCount
		if b.Inode.Type == inode.EFifo {
			e.Type = rootfs.TypeFifo
			e.Mode |= fs.ModeNamedPipe
		} else {
			e.Type = rootfs.TypeSocket
			e.Mode |= fs.ModeSocket
		}

	default:
		return e, fmt.Errorf("%s: tipo de inodo squashfs desconocido %d", e.Path, b.Inode.Type)
	}
	return e, nil
}

// linuxRdev convierte el dev_t estilo Linux (minor 8 bits + major 8 bits
// antiguos, o el encoding nuevo) al par major/minor que usará mknod.
func linuxRdev(dev uint32) uint64 {
	major := (dev >> 8) & 0xfff
	minor := (dev & 0xff) | ((dev >> 12) & 0xfff00)
	return uint64(major)<<32 | uint64(minor)
}

func permToMode(perm uint16) fs.FileMode {
	m := fs.FileMode(perm & 0o777)
	if perm&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if perm&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if perm&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// plainReader oculta el método WriteTo de *squashfs.File para que io.Copy
// use la ruta secuencial Read.
//
// Motivo: FullReader.WriteTo (v1.4.1) hace `i++; delete(results, i)`, borrando
// el bloque SIGUIENTE en lugar del ya escrito. Con resultados fuera de orden
// el consumidor se queda esperando un bloque que ya nadie enviará → deadlock
// (y pérdida de datos cuando no bloquea). La ruta Read es secuencial y correcta.
type plainReader struct {
	f *squashfs.File
}

func (p plainReader) Read(b []byte) (int, error) { return p.f.Read(b) }

func (p plainReader) Close() error { return p.f.Close() }

// Open resuelve la ruta componente a componente para esquivar el bug de
// filepath.Clean en Windows.
func (s *squashSource) Open(p string) (io.ReadCloser, error) {
	fsys, err := s.fsFor(path.Dir(p))
	if err != nil {
		return nil, err
	}
	f, err := fsys.OpenFile(path.Base(p))
	if err != nil {
		return nil, err
	}
	if f.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("%s: es un directorio", p)
	}
	return plainReader{f: f}, nil
}

func (s *squashSource) fsFor(dir string) (squashfs.FS, error) {
	dir = strings.TrimPrefix(dir, "./")
	if s.hasCache && s.cachePath == dir {
		return s.cacheFS, nil
	}

	cur := s.r.FS
	if dir != "" && dir != "." {
		for _, comp := range strings.Split(dir, "/") {
			f, err := cur.OpenFile(comp)
			if err != nil {
				return squashfs.FS{}, fmt.Errorf("abrir %q: %w", comp, err)
			}
			if !f.IsDir() {
				return squashfs.FS{}, fmt.Errorf("%q no es directorio", comp)
			}
			cur, err = f.FS()
			if err != nil {
				return squashfs.FS{}, err
			}
		}
	}

	s.cachePath, s.cacheFS, s.hasCache = dir, cur, true
	return cur, nil
}
