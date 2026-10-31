// Package ext4fs formatea y puebla un volumen ext4 desde espacio de usuario.
//
// Se apoya en github.com/diskfs/go-diskfs, que ya trae un escritor de ext4 en Go
// puro validado con e2fsck. Este paquete le pone encima dos cosas: una interfaz
// propia sobre blockdev, para que el mismo codigo sirva para una particion de un
// .img y para un disco fisico, y la lectura del superbloque para conocer UUID y
// etiqueta sin depender de nada externo.
package ext4fs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"time"

	"github.com/diskfs/go-diskfs/backend"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/google/uuid"

	"distrogo/pkg/blockdev"
)

const (
	// MinSize es el minimo con el que tiene sentido formatear: por debajo, la
	// tabla de inodos y los metadatos se comen el volumen.
	MinSize = 8 << 20

	// DefaultBlockSize es el tamano de bloque que se usa al formatear.
	DefaultBlockSize = 4096

	// inodeRatio es el mismo que usa mke2fs por defecto: un inodo cada 16 KiB.
	inodeRatio = 16384

	// minInodes es el suelo cuando el volumen es muy pequeno.
	minInodes = 1024

	// inodeBytes es lo que ocupa cada inodo en la tabla. Sirve para rechazar
	// combinaciones de tamano e inodos que no caben.
	inodeBytes = 256

	// sectorSize512 es lo unico que acepta go-diskfs para ext4.
	sectorSize512 = 512
)

var (
	// ErrBadSize indica un tamano de volumen invalido.
	ErrBadSize = errors.New("tamano de volumen invalido")
	// ErrNotExt4 indica que el volumen no contiene un ext4.
	ErrNotExt4 = errors.New("el volumen no contiene un ext4")
	// ErrNotWritable indica que se intenta formatear sobre algo de solo lectura.
	ErrNotWritable = errors.New("el dispositivo no admite escritura")
)

// Options ajusta el formateo.
type Options struct {
	// Label es la etiqueta del volumen.
	Label string
	// UUID en forma canonica. Vacio genera uno aleatorio.
	UUID string
	// InodeCount fija el numero de inodos. Cero lo calcula como mke2fs: uno cada
	// 16 KiB de volumen. El instalador debe pasar el numero real que necesita el
	// arbol que va a volcar.
	InodeCount uint32
	// Journal activa el diario. Por defecto no: para un PoC recien instalado no
	// aporta y ocupa espacio y tiempo.
	Journal bool
}

// Info resume un volumen ext4 leido de su superbloque.
type Info struct {
	UUID       string
	Label      string
	BlockSize  int
	InodeCount uint32
	SizeBytes  int64
	// FreeBytes sale del contador de bloques libres del superbloque. Se lee la
	// mitad baja: suficiente para los volumenes de este PoC, no para uno de 16 TiB.
	FreeBytes int64
}

// FS es un volumen ext4 abierto.
type FS struct {
	dev    blockdev.Writable
	st     *storage
	efs    *ext4.FileSystem
	offset int64
	size   int64
}

// Format crea un ext4 vacio en el rango indicado del dispositivo.
func Format(dev blockdev.Writable, offset, size int64, opts Options) (*FS, error) {
	if dev == nil {
		return nil, fmt.Errorf("formatear ext4: %w", ErrNotWritable)
	}
	if offset < 0 || size < MinSize {
		return nil, fmt.Errorf("%w: offset %d, tamano %d (minimo %d)", ErrBadSize, offset, size, MinSize)
	}
	if offset+size > dev.Size() {
		return nil, fmt.Errorf("%w: offset %d + tamano %d pasan de %d", ErrBadSize, offset, size, dev.Size())
	}
	if dev.SectorSize() != sectorSize512 {
		return nil, fmt.Errorf("%w: sector de %d bytes, solo se admite %d",
			ErrBadSize, dev.SectorSize(), sectorSize512)
	}

	id := uuid.New()
	if opts.UUID != "" {
		parsed, err := uuid.Parse(opts.UUID)
		if err != nil {
			return nil, fmt.Errorf("UUID %q: %w", opts.UUID, err)
		}
		id = parsed
	}

	inodes := opts.InodeCount
	if inodes == 0 {
		inodes = defaultInodeCount(size)
	}
	if table := int64(inodes) * inodeBytes; table > size/2 {
		return nil, fmt.Errorf("%w: %d inodos ocupan %s de tabla y el volumen es %s",
			ErrBadSize, inodes, blockdev.HumanSize(table), blockdev.HumanSize(size))
	}

	st := &storage{dev: dev, offset: offset, size: size}

	params := &ext4.Params{
		VolumeName:      opts.Label,
		UUID:            &id,
		InodeCount:      inodes,
		SectorsPerBlock: uint8(DefaultBlockSize / sectorSize512),
	}
	// Sin diario: no aporta en un volumen recien creado y ocupa espacio.
	if !opts.Journal {
		params.Features = append(params.Features, ext4.WithFeatureHasJournal(false))
	}
	// Sin resize_inode: go-diskfs no sabe colocar sus bloques de respaldo cuando el
	// volumen tiene un solo grupo, y nosotros no redimensionamos en caliente.
	params.Features = append(params.Features, ext4.WithFeatureReservedGDTBlocksForExpansion(false))

	efs, err := ext4.Create(st, size, 0, sectorSize512, params)
	if err != nil {
		return nil, fmt.Errorf("formatear ext4: %w", err)
	}

	return &FS{dev: dev, st: st, efs: efs, offset: offset, size: size}, nil
}

// Open monta un ext4 existente para leerlo o escribir en el.
func Open(dev blockdev.Writable, offset, size int64) (*FS, error) {
	if offset < 0 || size < MinSize || offset+size > dev.Size() {
		return nil, fmt.Errorf("%w: offset %d, tamano %d", ErrBadSize, offset, size)
	}
	if dev.SectorSize() != sectorSize512 {
		return nil, fmt.Errorf("%w: sector de %d bytes", ErrBadSize, dev.SectorSize())
	}

	st := &storage{dev: dev, offset: offset, size: size}
	efs, err := ext4.Read(st, size, 0, sectorSize512)
	if err != nil {
		return nil, fmt.Errorf("abrir ext4: %w", err)
	}
	return &FS{dev: dev, st: st, efs: efs, offset: offset, size: size}, nil
}

// Info lee el superbloque del volumen.
func (f *FS) Info() (Info, error) {
	return readInfo(f.dev, f.offset, f.size)
}

// Close limpia y sincroniza.
func (f *FS) Close() error {
	if f.efs != nil {
		if err := f.efs.Close(); err != nil {
			return fmt.Errorf("cerrar ext4: %w", err)
		}
		f.efs = nil
	}
	if err := f.dev.Sync(); err != nil {
		return fmt.Errorf("sincronizar el volumen: %w", err)
	}
	return nil
}

// Mkdir crea un directorio y los padres que falten.
func (f *FS) Mkdir(path string) error {
	if f.efs == nil {
		return errors.New("el volumen esta cerrado")
	}
	if err := f.efs.Mkdir(path); err != nil {
		return fmt.Errorf("crear el directorio %q: %w", path, err)
	}
	return nil
}

// WriteFile copia r en un fichero, creando el directorio padre si falta.
func (f *FS) WriteFile(path string, r io.Reader) error {
	if f.efs == nil {
		return errors.New("el volumen esta cerrado")
	}

	// go-diskfs no crea los padres: hay que hacerlo antes.
	if dir := pathpkg.Dir(path); dir != "." && dir != "/" {
		if err := f.Mkdir(dir); err != nil {
			return err
		}
	}

	file, err := f.efs.OpenFile(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("crear %q: %w", path, err)
	}

	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(file, r, buf); err != nil {
		_ = file.Close()
		return fmt.Errorf("escribir %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cerrar %q: %w", path, err)
	}
	return nil
}

// --------------------------------------------------------------- superbloque

// readInfo lee los pocos campos del superbloque que hacen falta para /etc/fstab
// y para informar al usuario. El superbloque empieza en el byte 1024.
func readInfo(dev blockdev.Device, offset, size int64) (Info, error) {
	buf := make([]byte, 1024)
	if _, err := dev.ReadAt(buf, offset+1024); err != nil {
		return Info{}, fmt.Errorf("leer el superbloque: %w", err)
	}

	if magic := binary.LittleEndian.Uint16(buf[0x38:]); magic != 0xEF53 {
		return Info{}, fmt.Errorf("%w: magic 0x%04X", ErrNotExt4, magic)
	}

	logBlock := binary.LittleEndian.Uint32(buf[0x18:])
	if logBlock > 6 {
		return Info{}, fmt.Errorf("%w: tamano de bloque absurdo (%d)", ErrNotExt4, logBlock)
	}

	blockSize := int64(1024 << logBlock)
	freeBlocks := int64(binary.LittleEndian.Uint32(buf[0x0C:]))

	return Info{
		UUID:       formatUUID(buf[0x68:0x78]),
		Label:      cstring(buf[0x78:0x88]),
		BlockSize:  int(blockSize),
		InodeCount: binary.LittleEndian.Uint32(buf[0x00:]),
		SizeBytes:  size,
		FreeBytes:  freeBlocks * blockSize,
	}, nil
}

// formatUUID pasa los 16 bytes del superbloque a la forma canonica.
func formatUUID(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// cstring corta en el primer NUL.
func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// defaultInodeCount imita a mke2fs: uno cada 16 KiB, con un minimo.
func defaultInodeCount(size int64) uint32 {
	n := size / inodeRatio
	if n < minInodes {
		n = minInodes
	}
	if n > int64(^uint32(0)) {
		n = int64(^uint32(0))
	}
	return uint32(n)
}

// ------------------------------------------------------------------ storage

// storage adapta un blockdev.Writable a la interfaz que espera go-diskfs,
// recortado al rango de la particion.
type storage struct {
	dev    blockdev.Writable
	offset int64
	size   int64
	pos    int64
}

var _ backend.Storage = (*storage)(nil)

func (s *storage) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > s.size {
		return 0, fmt.Errorf("leer en offset %d de una seccion de %d: %w", off, s.size, blockdev.ErrOutOfRange)
	}
	return s.dev.ReadAt(p, s.offset+off)
}

func (s *storage) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > s.size {
		return 0, fmt.Errorf("escribir %d bytes en offset %d de una seccion de %d: %w",
			len(p), off, s.size, blockdev.ErrOutOfRange)
	}
	return s.dev.WriteAt(p, s.offset+off)
}

func (s *storage) Read(p []byte) (int, error) {
	n, err := s.ReadAt(p, s.pos)
	s.pos += int64(n)
	return n, err
}

func (s *storage) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.pos + offset
	case io.SeekEnd:
		abs = s.size + offset
	default:
		return 0, fmt.Errorf("whence invalido: %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("seek negativo: %d", abs)
	}
	s.pos = abs
	return abs, nil
}

func (s *storage) Stat() (fs.FileInfo, error) { return sectionInfo{s}, nil }

func (s *storage) Close() error { return nil }

func (s *storage) Sys() (*os.File, error) {
	if sy, ok := s.dev.(interface{ Sys() (*os.File, error) }); ok {
		return sy.Sys()
	}
	return nil, errors.New("el dispositivo no expone un descriptor del sistema")
}

func (s *storage) Writable() (backend.WritableFile, error) { return s, nil }

func (s *storage) Path() string { return "" }

// sectionInfo describe la seccion como fichero para go-diskfs.
type sectionInfo struct{ s *storage }

func (i sectionInfo) Name() string       { return "ext4" }
func (i sectionInfo) Size() int64        { return i.s.size }
func (i sectionInfo) Mode() fs.FileMode  { return 0o644 }
func (i sectionInfo) ModTime() time.Time { return time.Time{} }
func (i sectionInfo) IsDir() bool        { return false }
func (i sectionInfo) Sys() any           { return nil }
