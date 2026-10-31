package blockdev

import (
	"errors"
	"fmt"
	"os"
)

// DefaultSectorSize es el tamaño de sector que asumimos para un fichero de
// imagen. Los dispositivos reales informan el suyo (pkg/windisk).
const DefaultSectorSize = 512

var (
	// ErrNotWritable se devuelve al intentar escribir en un dispositivo abierto
	// en modo lectura.
	ErrNotWritable = errors.New("el dispositivo es de solo lectura")
	// ErrOutOfRange se devuelve cuando una escritura excederia el final del
	// dispositivo. Un dispositivo tiene tamaño fijo: no crece como un fichero.
	ErrOutOfRange = errors.New("escritura fuera de los limites del dispositivo")
	// ErrInvalidSize se devuelve al crear un dispositivo con tamaño no positivo.
	ErrInvalidSize = errors.New("tamano de dispositivo invalido")
)

// Device es un dispositivo de bloques de solo lectura o un fichero que lo
// simula. Todo el pipeline (GPT, FAT32, ext4) trabaja contra esta interfaz.
//
// Las interfaces estan separadas a proposito: quien solo necesita leer no puede
// escribir ni por accidente, porque el metodo no existe en su tipo.
type Device interface {
	// ReadAt lee en p, empezando en el byte off. Devuelve io.EOF si se agota el
	// dispositivo antes de llenar p.
	ReadAt(p []byte, off int64) (int, error)
	// Size es el tamaño total en bytes.
	Size() int64
	// SectorSize es el tamaño de sector logico en bytes. Es el valor al que deben
	// alinearse los bufferes y los offsets cuando se abre el dispositivo real sin
	// buffer intermedio.
	SectorSize() int
	// Sync fuerza el volcado a disco.
	Sync() error
	// Close libera el descriptor.
	Close() error
}

// Writable es un Device que además admite escritura.
type Writable interface {
	Device
	WriteAt(p []byte, off int64) (int, error)
}

// BusType es el tipo de bus por el que esta conectado un disco.
type BusType uint8

const (
	BusUnknown BusType = iota
	BusSATA
	BusNVMe
	BusUSB
	BusRAID
	BusVirtual
	BusSD
)

func (b BusType) String() string {
	switch b {
	case BusSATA:
		return "sata"
	case BusNVMe:
		return "nvme"
	case BusUSB:
		return "usb"
	case BusRAID:
		return "raid"
	case BusVirtual:
		return "virtual"
	case BusSD:
		return "sd"
	default:
		return "desconocido"
	}
}

// Disk es el inventario de un disco fisico. Son datos: no toma decisiones.
// Decidir si es seguro escribir en el es el trabajo de pkg/safety.
type Disk struct {
	Number         uint32
	Model          string
	Serial         string
	Bus            BusType
	SizeBytes      int64
	LogicalSector  int
	PhysicalSector int
	Removable      bool
	ReadOnly       bool
	IsSystem       bool
	IsBoot         bool
	Partitions     []Partition
}

// Partition es una particion de un disco, en bytes.
type Partition struct {
	Number    uint32
	Offset    int64
	SizeBytes int64
	TypeGUID  string
	// Volume es nil si la particion no tiene un volumen reconocible montado.
	Volume *Volume
}

// Volume es un volumen reconocido por el sistema operativo y su punto de montaje.
type Volume struct {
	GUID       string
	Label      string
	FileSystem string
	// MountPoint es la letra ("C:\") o el punto de montaje. Vacio si no esta montado.
	MountPoint string
	DiskNumber uint32
	// Offset es donde empieza el volumen dentro del disco, en bytes.
	Offset int64
}

// Mounted indica si el volumen esta montado con un punto de acceso.
func (v Volume) Mounted() bool { return v.MountPoint != "" }

// Image es un Device respaldado por un fichero, para desarrollo y tests.
//
// TODO(DG-18): en NTFS, truncar reserva el espacio real (una imagen de 466 GiB
// ocupa 466 GiB). Activar FSCTL_SET_SPARSE antes de intentar replicar el disco
// objetivo a escala 1:1.
type Image struct {
	f        *os.File
	path     string
	size     int64
	sector   int
	writable bool
}

var (
	_ Device   = (*Image)(nil)
	_ Writable = (*Image)(nil)
)

// OpenImage abre una imagen existente. Si writable es false el fichero se abre
// en solo lectura y WriteAt devuelve ErrNotWritable.
func OpenImage(path string, writable bool) (*Image, error) {
	flag := os.O_RDONLY
	if writable {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, fmt.Errorf("abrir imagen %q: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat de la imagen %q: %w", path, err)
	}
	return &Image{
		f:        f,
		path:     path,
		size:     st.Size(),
		sector:   DefaultSectorSize,
		writable: writable,
	}, nil
}

// CreateImage crea o trunca una imagen del tamano indicado. Ojo: trunca. Si el
// fichero existe, su contenido se pierde.
func CreateImage(path string, size int64) (*Image, error) {
	if size <= 0 {
		return nil, fmt.Errorf("crear imagen %q: %w (%d)", path, ErrInvalidSize, size)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("crear imagen %q: %w", path, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("truncar imagen %q a %d bytes: %w", path, size, err)
	}
	return &Image{
		f:        f,
		path:     path,
		size:     size,
		sector:   DefaultSectorSize,
		writable: true,
	}, nil
}

func (i *Image) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("leer imagen %q en offset %d: %w", i.path, off, os.ErrInvalid)
	}
	n, err := i.f.ReadAt(p, off)
	if err != nil {
		return n, fmt.Errorf("leer imagen %q en offset %d: %w", i.path, off, err)
	}
	return n, nil
}

func (i *Image) WriteAt(p []byte, off int64) (int, error) {
	if !i.writable {
		return 0, fmt.Errorf("escribir imagen %q: %w", i.path, ErrNotWritable)
	}
	if off < 0 {
		return 0, fmt.Errorf("escribir imagen %q en offset %d: %w", i.path, off, os.ErrInvalid)
	}
	if off+int64(len(p)) > i.size {
		return 0, fmt.Errorf("escribir %d bytes en offset %d de una imagen de %d bytes: %w",
			len(p), off, i.size, ErrOutOfRange)
	}
	n, err := i.f.WriteAt(p, off)
	if err != nil {
		return n, fmt.Errorf("escribir imagen %q en offset %d: %w", i.path, off, err)
	}
	return n, nil
}

func (i *Image) Size() int64 { return i.size }

func (i *Image) SectorSize() int { return i.sector }

// SetSectorSize permite simular un dispositivo de 4096 bytes de sector fisico.
// Solo afecta a lo que informa SectorSize, no a la alineacion real del fichero.
func (i *Image) SetSectorSize(n int) error {
	if n <= 0 {
		return fmt.Errorf("tamano de sector invalido: %d", n)
	}
	i.sector = n
	return nil
}

func (i *Image) Sync() error {
	if err := i.f.Sync(); err != nil {
		return fmt.Errorf("sincronizar imagen %q: %w", i.path, err)
	}
	return nil
}

func (i *Image) Close() error {
	if err := i.f.Close(); err != nil {
		return fmt.Errorf("cerrar imagen %q: %w", i.path, err)
	}
	return nil
}

// HumanSize formatea un numero de bytes para mostrarlo. Es solo presentacion:
// no redondea los valores que se guardan en disco.
func HumanSize(n int64) string {
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
