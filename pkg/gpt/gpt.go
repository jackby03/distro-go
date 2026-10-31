// Package gpt construye y valida tablas de particiones GPT en memoria: GUIDs,
// geometria, asignacion de rangos de LBA y comprobaciones de solape y limites.
//
// No conoce Win32 ni escribe nada: serializar los bytes y publicarlos en un
// dispositivo es trabajo de pkg/gpt/io.go y de pkg/windisk.
package gpt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

const (
	// EntryCount es el numero de entradas de la tabla. 128 es el tamano de facto
	// en GPT.
	EntryCount = 128
	// EntrySize es el tamano fijo de una entrada GPT, en bytes.
	EntrySize = 128
	// HeaderSize es el tamano de la cabecera GPT.
	HeaderSize = 92
	// DefaultAlignment es la alineacion de las particiones: 1 MiB. Coincide con
	// lo que hacen los instaladores reales y evita el penalizacion de rendimiento
	// de las unidades de estado solido.
	DefaultAlignment = 1 << 20
	// MaxNameUnits es la longitud maxima del nombre de una particion, en unidades
	// UTF-16. Los caracteres fuera del plano basico ocupan dos.
	MaxNameUnits = 36
)

var (
	// ErrInvalidLayout indica que el disco es demasiado pequeno o el sector no es
	// valido.
	ErrInvalidLayout = errors.New("layout GPT invalido")
	// ErrOverlap indica particiones que se pisan.
	ErrOverlap = errors.New("particiones solapadas")
	// ErrOutOfRange indica una particion fuera del area util.
	ErrOutOfRange = errors.New("particion fuera del area util")
	// ErrTooBig indica que las particiones no caben en el disco.
	ErrTooBig = errors.New("las particiones no caben en el disco")
	// ErrBadGUID indica un GUID con formato invalido.
	ErrBadGUID = errors.New("GUID invalido")
)

// ------------------------------------------------------------------------ GUID

// GUID es un identificador de 16 bytes en el orden en que se guarda en disco:
// los tres primeros campos en little endian y los dos ultimos literalmente.
type GUID [16]byte

// String devuelve la forma canonica en minusculas y sin llaves, la misma que
// muestran fdisk y sgdisk.
func (g GUID) String() string {
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		g[3], g[2], g[1], g[0],
		g[5], g[4],
		g[7], g[6],
		g[8], g[9],
		g[10], g[11], g[12], g[13], g[14], g[15])
}

// IsZero indica si el GUID esta todo a cero, que en GPT significa "sin asignar".
func (g GUID) IsZero() bool {
	var zero GUID
	return g == zero
}

// ParseGUID acepta la forma canonica con o sin guiones y con o sin llaves.
func ParseGUID(s string) (GUID, error) {
	t := strings.TrimSpace(strings.ToLower(s))
	t = strings.Trim(t, "{}")
	t = strings.ReplaceAll(t, "-", "")
	if len(t) != 32 {
		return GUID{}, fmt.Errorf("%w: %q", ErrBadGUID, s)
	}
	raw, err := hex.DecodeString(t)
	if err != nil {
		return GUID{}, fmt.Errorf("%w: %q", ErrBadGUID, s)
	}
	var g GUID
	g[0], g[1], g[2], g[3] = raw[3], raw[2], raw[1], raw[0]
	g[4], g[5] = raw[5], raw[4]
	g[6], g[7] = raw[7], raw[6]
	copy(g[8:], raw[8:])
	return g, nil
}

// NewGUID genera un GUID aleatorio version 4.
func NewGUID() (GUID, error) {
	var g GUID
	if _, err := rand.Read(g[:]); err != nil {
		return GUID{}, fmt.Errorf("generar GUID: %w", err)
	}
	g[7] = g[7]&0x0f | 0x40 // version 4
	g[8] = g[8]&0x3f | 0x80 // variante RFC 4122
	return g, nil
}

// Tipos de particion que usa el PoC. Estan en el orden de disco para que
// coincidan con lo que ve el kernel.
var (
	// TypeESP es la particion de sistema EFI: C12A7328-F81F-11D2-BA4B-00A0C93EC93B
	TypeESP = GUID{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}
	// TypeLinuxFilesystem es el tipo generico de datos Linux: 0FC63DAF-8483-4772-8E79-3D69D8477DE4
	TypeLinuxFilesystem = GUID{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47, 0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4}
	// TypeLinuxSwap es la swap de Linux: 0657FD6D-A4AB-43C4-84E5-0933C84B4F4F
	TypeLinuxSwap = GUID{0x6d, 0xfd, 0x57, 0x06, 0xab, 0xa4, 0xc4, 0x43, 0x84, 0xe5, 0x09, 0x33, 0xc8, 0x4b, 0x4f, 0x4f}
	// TypeMicrosoftBasicData es la particion de datos de Windows: EBD0A0A2-B9E5-4433-87C0-68B6B72699C7
	TypeMicrosoftBasicData = GUID{0xa2, 0xa0, 0xd0, 0xeb, 0xe5, 0xb9, 0x33, 0x44, 0x87, 0xc0, 0x68, 0xb6, 0xb7, 0x26, 0x99, 0xc7}
)

// -------------------------------------------------------------------- geometria

// Layout es el reparto de un disco segun GPT: donde empieza y acaba el area
// util, y cuanto ocupa la tabla de entradas.
type Layout struct {
	SectorSize   int
	TotalSectors uint64
	EntrySectors uint64
	FirstUsable  uint64
	LastUsable   uint64
}

// NewLayout calcula el reparto a partir del tamano del disco.
func NewLayout(sizeBytes int64, sectorSize int) (Layout, error) {
	if sectorSize < 512 || sectorSize > 4096 || sectorSize&(sectorSize-1) != 0 {
		return Layout{}, fmt.Errorf("%w: sector de %d bytes", ErrInvalidLayout, sectorSize)
	}
	if sizeBytes <= 0 {
		return Layout{}, fmt.Errorf("%w: tamano %d", ErrInvalidLayout, sizeBytes)
	}

	total := uint64(sizeBytes) / uint64(sectorSize)
	entryBytes := uint64(EntryCount * EntrySize)
	entrySectors := (entryBytes + uint64(sectorSize) - 1) / uint64(sectorSize)

	// LBA 0 es el MBR protector, LBA 1 la cabecera primaria y detras las
	// entradas. Al final: entradas de respaldo y la cabecera de respaldo.
	first := 2 + entrySectors
	last := total - entrySectors - 2
	if total <= first+entrySectors+2 || last < first {
		return Layout{}, fmt.Errorf("%w: %d sectores no dan para una tabla GPT", ErrInvalidLayout, total)
	}

	return Layout{
		SectorSize:   sectorSize,
		TotalSectors: total,
		EntrySectors: entrySectors,
		FirstUsable:  first,
		LastUsable:   last,
	}, nil
}

// AlignSectors es la alineacion en sectores. Nunca es menor que uno.
func (l Layout) AlignSectors() uint64 {
	a := uint64(DefaultAlignment) / uint64(l.SectorSize)
	if a == 0 {
		return 1
	}
	return a
}

// UsableSectors es el numero de sectores disponibles para particiones.
func (l Layout) UsableSectors() uint64 {
	if l.LastUsable < l.FirstUsable {
		return 0
	}
	return l.LastUsable - l.FirstUsable + 1
}

// Entry es una particion de la tabla.
type Entry struct {
	Type       GUID
	Unique     GUID
	FirstLBA   uint64
	LastLBA    uint64
	Attributes uint64
	Name       string
}

// Sectors es el tamano de la particion en sectores.
func (e Entry) Sectors() uint64 {
	if e.LastLBA < e.FirstLBA {
		return 0
	}
	return e.LastLBA - e.FirstLBA + 1
}

// Bytes es el tamano de la particion en bytes.
func (e Entry) Bytes(sectorSize int) int64 {
	return int64(e.Sectors()) * int64(sectorSize)
}

// SizeSpec es el tamano que pide el llamante para una particion.
type SizeSpec struct {
	Name string
	Type GUID
	// SizeBytes a cero significa "todo el espacio que quede", y solo vale para
	// la ultima particion.
	SizeBytes  int64
	Attributes uint64
}

// Plan reparte las particiones pedidas, alineadas a 1 MiB, sin solapes.
func Plan(l Layout, specs []SizeSpec) ([]Entry, error) {
	align := l.AlignSectors()
	cursor := alignUp(l.FirstUsable, align)
	out := make([]Entry, 0, len(specs))

	for i, s := range specs {
		var sectors uint64
		if s.SizeBytes <= 0 {
			if i != len(specs)-1 {
				return nil, fmt.Errorf("%w: solo la ultima particion puede pedir el resto del disco", ErrTooBig)
			}
			if cursor > l.LastUsable {
				return nil, fmt.Errorf("%w: no queda espacio para la particion %d", ErrTooBig, i+1)
			}
			sectors = l.LastUsable - cursor + 1
		} else {
			sectors = alignUp(uint64(s.SizeBytes)/uint64(l.SectorSize), align)
		}

		if sectors == 0 {
			return nil, fmt.Errorf("%w: la particion %d pide 0 sectores", ErrTooBig, i+1)
		}
		last := cursor + sectors - 1
		if last > l.LastUsable {
			return nil, fmt.Errorf("%w: la particion %d (%q) acabaria en el sector %d y el area util termina en %d",
				ErrTooBig, i+1, s.Name, last, l.LastUsable)
		}

		out = append(out, Entry{
			Type:       s.Type,
			FirstLBA:   cursor,
			LastLBA:    last,
			Attributes: s.Attributes,
			Name:       s.Name,
		})
		cursor = alignUp(last+1, align)
	}
	return out, nil
}

// Table es una tabla GPT: el GUID del disco y sus particiones.
type Table struct {
	DiskGUID   GUID
	Partitions []Entry
}

// NewTable envuelve las particiones y rellena con GUIDs aleatorios los que
// falten. La validacion contra un layout concreto se hace despues, porque
// depende del disco.
func NewTable(diskGUID GUID, parts []Entry) (*Table, error) {
	t := &Table{
		DiskGUID:   diskGUID,
		Partitions: append([]Entry(nil), parts...),
	}
	if t.DiskGUID.IsZero() {
		g, err := NewGUID()
		if err != nil {
			return nil, err
		}
		t.DiskGUID = g
	}
	for i := range t.Partitions {
		if !t.Partitions[i].Unique.IsZero() {
			continue
		}
		g, err := NewGUID()
		if err != nil {
			return nil, err
		}
		t.Partitions[i].Unique = g
	}
	return t, nil
}

// Validate comprueba que la tabla cabe en el disco y no tiene incoherencias.
func (t *Table) Validate(l Layout) error {
	if t.DiskGUID.IsZero() {
		return fmt.Errorf("%w: el disco no tiene GUID", ErrInvalidLayout)
	}

	for i, e := range t.Partitions {
		if e.Type.IsZero() {
			return fmt.Errorf("%w: la particion %d no tiene tipo", ErrInvalidLayout, i+1)
		}
		if e.Unique.IsZero() {
			return fmt.Errorf("%w: la particion %d no tiene GUID unico", ErrInvalidLayout, i+1)
		}
		if units := len(utf16.Encode([]rune(e.Name))); units > MaxNameUnits {
			return fmt.Errorf("%w: el nombre de la particion %d ocupa %d unidades UTF-16, maximo %d",
				ErrInvalidLayout, i+1, units, MaxNameUnits)
		}
		if e.LastLBA < e.FirstLBA {
			return fmt.Errorf("%w: la particion %d acaba antes de empezar", ErrOutOfRange, i+1)
		}
		if e.FirstLBA < l.FirstUsable || e.LastLBA > l.LastUsable {
			return fmt.Errorf("%w: la particion %d ocupa %d..%d y el area util es %d..%d",
				ErrOutOfRange, i+1, e.FirstLBA, e.LastLBA, l.FirstUsable, l.LastUsable)
		}
	}

	sorted := append([]Entry(nil), t.Partitions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FirstLBA < sorted[j].FirstLBA })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].FirstLBA <= sorted[i-1].LastLBA {
			return fmt.Errorf("%w: la particion %d empieza en %d y la anterior acaba en %d",
				ErrOverlap, i+1, sorted[i].FirstLBA, sorted[i-1].LastLBA)
		}
	}
	return nil
}

func alignUp(v, a uint64) uint64 {
	if a <= 1 {
		return v
	}
	if rem := v % a; rem != 0 {
		return v + a - rem
	}
	return v
}
