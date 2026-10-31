// Package squashfs lee un rootfs SquashFS y lo expone como rootfs.Source.
//
// Se apoya en github.com/CalebQ42/squashfs, que hace el trabajo de parsear el
// formato y descomprimir, pero con tres rodeos obligatorios que se explican donde
// se aplican: rutas anidadas en Windows, un deadlock en la escritura multihilo y
// los ficheros de cero bytes.
//
// No escribe nada: entrega el arbol en streaming con buffer acotado.
package squashfs

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	sqfs "github.com/CalebQ42/squashfs"
	squashfslow "github.com/CalebQ42/squashfs/low"
	"github.com/CalebQ42/squashfs/low/directory"
	"github.com/CalebQ42/squashfs/low/inode"

	"distrogo/pkg/rootfs"
)

// FS es un volumen SquashFS abierto.
type FS struct {
	r *sqfs.Reader

	// Cache de un nivel: el recorrido es en profundidad, asi que los ficheros de
	// un mismo directorio llegan seguidos y el padre se resuelve una sola vez.
	cachePath string
	cacheFS   sqfs.FS
	hasCache  bool
}

// Info resume el volumen.
type Info struct {
	Version     string
	Compression string
	BlockSize   uint32
	Inodes      uint32
	Fragments   uint32
	IDCount     uint16
	Flags       uint16
	BytesUsed   uint64
}

// Open lee la cabecera de un SquashFS. r puede ser una seccion de un ISO: no
// hace falta extraerlo a un fichero.
func Open(r io.ReaderAt) (*FS, error) {
	rdr, err := sqfs.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("abrir el SquashFS: %w", err)
	}
	return &FS{r: &rdr}, nil
}

// Info devuelve los datos del superbloque.
func (f *FS) Info() Info {
	sb := f.r.Low.Superblock
	return Info{
		Version:     fmt.Sprintf("%d.%d", sb.VerMaj, sb.VerMin),
		Compression: compressionName(sb.CompType),
		BlockSize:   sb.BlockSize,
		Inodes:      sb.InodeCount,
		Fragments:   sb.FragCount,
		IDCount:     sb.IdCount,
		Flags:       sb.Flags,
		BytesUsed:   sb.Size,
	}
}

// Walk recorre el arbol y entrega padres antes que hijos.
func (f *FS) Walk(fn func(rootfs.Entry) error) error {
	return f.walkDir(f.r.Low.Root, "", fn)
}

func (f *FS) walkDir(d squashfslow.Directory, prefix string, fn func(rootfs.Entry) error) error {
	for _, de := range d.Entries {
		// SquashFS guarda "." y ".." como entradas reales.
		if de.Name == "." || de.Name == ".." {
			continue
		}

		base, err := f.r.Low.BaseFromEntry(de)
		if err != nil {
			return fmt.Errorf("%s: leer el inodo: %w", joinPath(prefix, de.Name), err)
		}

		e, err := f.entry(base, prefix, de)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}

		if e.Type == rootfs.TypeDir {
			sub, err := base.ToDir(f.r.Low)
			if err != nil {
				return fmt.Errorf("%s: leer el directorio: %w", e.Path, err)
			}
			if err := f.walkDir(sub, e.Path, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// entry traduce un inodo de SquashFS al contrato rootfs.
func (f *FS) entry(base squashfslow.FileBase, prefix string, de directory.Entry) (rootfs.Entry, error) {
	e := rootfs.Entry{
		Path:    joinPath(prefix, de.Name),
		Inode:   uint64(base.Inode.Num),
		ModTime: time.Unix(int64(base.Inode.ModTime), 0),
		Nlink:   1,
		Mode:    permToMode(base.Inode.Perm),
	}

	uid, err := base.Uid(&f.r.Low)
	if err != nil {
		return e, fmt.Errorf("%s: uid: %w", e.Path, err)
	}
	gid, err := base.Gid(&f.r.Low)
	if err != nil {
		return e, fmt.Errorf("%s: gid: %w", e.Path, err)
	}
	e.UID, e.GID = uid, gid

	switch base.Inode.Type {
	case inode.Dir, inode.EDir:
		e.Type = rootfs.TypeDir
		e.Mode |= fs.ModeDir

	case inode.Fil:
		e.Type = rootfs.TypeRegular
		e.Size = int64(base.Inode.Data.(inode.File).Size)
	case inode.EFil:
		file := base.Inode.Data.(inode.EFile)
		e.Type = rootfs.TypeRegular
		e.Size = int64(file.Size)
		e.Nlink = file.LinkCount

	case inode.Sym:
		sym := base.Inode.Data.(inode.Symlink)
		e.Type = rootfs.TypeSymlink
		e.Link = string(sym.Target)
		e.Nlink = sym.LinkCount
		e.Mode |= fs.ModeSymlink
	case inode.ESym:
		sym := base.Inode.Data.(inode.ESymlink)
		e.Type = rootfs.TypeSymlink
		e.Link = string(sym.Target)
		e.Nlink = sym.LinkCount
		e.Mode |= fs.ModeSymlink

	case inode.Block, inode.Char:
		dev := base.Inode.Data.(inode.Device)
		e.Type = rootfs.TypeDevice
		e.Rdev = linuxRdev(dev.Dev)
		e.Nlink = dev.LinkCount
		e.Mode |= fs.ModeDevice
		if base.Inode.Type == inode.Char {
			e.Mode |= fs.ModeCharDevice
		}
	case inode.EBlock, inode.EChar:
		dev := base.Inode.Data.(inode.EDevice)
		e.Type = rootfs.TypeDevice
		e.Rdev = linuxRdev(dev.Dev)
		e.Nlink = dev.LinkCount
		e.Mode |= fs.ModeDevice
		if base.Inode.Type == inode.EChar {
			e.Mode |= fs.ModeCharDevice
		}

	case inode.Fifo, inode.Sock:
		ipc := base.Inode.Data.(inode.IPC)
		e.Nlink = ipc.LinkCount
		if base.Inode.Type == inode.Fifo {
			e.Type = rootfs.TypeFifo
			e.Mode |= fs.ModeNamedPipe
		} else {
			e.Type = rootfs.TypeSocket
			e.Mode |= fs.ModeSocket
		}
	case inode.EFifo, inode.ESock:
		ipc := base.Inode.Data.(inode.EIPC)
		e.Nlink = ipc.LinkCount
		if base.Inode.Type == inode.EFifo {
			e.Type = rootfs.TypeFifo
			e.Mode |= fs.ModeNamedPipe
		} else {
			e.Type = rootfs.TypeSocket
			e.Mode |= fs.ModeSocket
		}

	default:
		return e, fmt.Errorf("%s: tipo de inodo desconocido %d", e.Path, base.Inode.Type)
	}

	return e, nil
}

// plainReader esconde WriteTo para que io.Copy use la ruta secuencial Read.
//
// FullReader.WriteTo de la libreria hace `i++; delete(results, i)` y borra el
// bloque siguiente en lugar del ya escrito. Con resultados fuera de orden el
// consumidor se queda esperando un bloque que nadie enviara: deadlock. La ruta
// Read es secuencial y correcta.
type plainReader struct {
	f *sqfs.File
}

func (p plainReader) Read(b []byte) (int, error) { return p.f.Read(b) }

func (p plainReader) Close() error { return p.f.Close() }

// Open devuelve el contenido de un fichero regular.
//
// La ruta se resuelve componente a componente porque FS.OpenFile usa
// filepath.Clean, que en Windows convierte "/" en "\" y rompe toda ruta con mas
// de un nivel.
func (f *FS) Open(p string) (io.ReadCloser, error) {
	fsys, err := f.fsFor(path.Dir(p))
	if err != nil {
		return nil, err
	}

	file, err := fsys.OpenFile(path.Base(p))
	if err != nil {
		return nil, fmt.Errorf("abrir %q: %w", p, err)
	}
	// El contrato solo llama a Open con ficheros regulares. Un symlink no tiene
	// contenido propio, y devolverlo vacio seria enganoso.
	if !file.IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%q no es un fichero regular", p)
	}
	// Un fichero vacio hace que la libreria devuelva "invalid block index":
	// su contenido es vacio, asi que no hay nada que leer.
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat de %q: %w", p, err)
	}
	if info.Size() == 0 {
		_ = file.Close()
		return io.NopCloser(strings.NewReader("")), nil
	}

	return plainReader{f: file}, nil
}

// fsFor resuelve un directorio navegando componente a componente.
func (f *FS) fsFor(dir string) (sqfs.FS, error) {
	dir = strings.TrimPrefix(dir, "./")
	if f.hasCache && f.cachePath == dir {
		return f.cacheFS, nil
	}

	cur := f.r.FS
	if dir != "" && dir != "." {
		for _, comp := range strings.Split(dir, "/") {
			file, err := cur.OpenFile(comp)
			if err != nil {
				return sqfs.FS{}, fmt.Errorf("abrir %q: %w", comp, err)
			}
			if !file.IsDir() {
				return sqfs.FS{}, fmt.Errorf("%q no es un directorio", comp)
			}
			next, err := file.FS()
			if err != nil {
				return sqfs.FS{}, fmt.Errorf("entrar en %q: %w", comp, err)
			}
			cur = next
		}
	}

	f.cachePath, f.cacheFS, f.hasCache = dir, cur, true
	return cur, nil
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
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

// linuxRdev convierte el dev_t estilo Linux en el par mayor/menor.
func linuxRdev(dev uint32) uint64 {
	major := (dev >> 8) & 0xfff
	minor := (dev & 0xff) | ((dev >> 12) & 0xfff00)
	return uint64(major)<<32 | uint64(minor)
}

func compressionName(c uint16) string {
	switch c {
	case squashfslow.ZlibCompression:
		return "gzip"
	case squashfslow.LZMACompression:
		return "lzma"
	case squashfslow.LZOCompression:
		return "lzo"
	case squashfslow.XZCompression:
		return "xz"
	case squashfslow.LZ4Compression:
		return "lz4"
	case squashfslow.ZSTDCompression:
		return "zstd"
	default:
		return fmt.Sprintf("desconocida(%d)", c)
	}
}
