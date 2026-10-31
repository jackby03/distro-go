//go:build windows

// Capa Win32 de windisk: constantes IOCTL, estructuras del ABI y helpers de
// consulta. No decide nada y no escribe nada: aqui solo se traducen bytes.
//
// Las estructuras NO vienen en x/sys/windows, hay que declararlas a mano, y un
// byte de desalineacion corrompe el comando de disco. Por eso cada una lleva su
// comprobacion de tamano y de offset en tiempo de compilacion: si el layout no
// cuadra, el paquete no compila. Es preferible a descubrirlo escribiendo en un
// disco real.
package windisk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"distrogo/pkg/blockdev"
)

// Codigos IOCTL tomados de winioctl.h. Solo los de consulta: los de escritura
// (SET_DRIVE_LAYOUT_EX, UPDATE_PROPERTIES) llegan en M7.
const (
	ioctlDiskGetDriveGeometryEx     = 0x000700a0
	ioctlDiskGetLengthInfo          = 0x0007405c
	ioctlDiskGetDriveLayoutEx       = 0x00070050
	ioctlDiskIsWritable             = 0x00070024
	ioctlStorageQueryProperty       = 0x002d1400
	ioctlVolumeGetVolumeDiskExtents = 0x00560000
)

// Identificadores de IOCTL_STORAGE_QUERY_PROPERTY.
const (
	storageDeviceProperty          = 0
	storageAccessAlignmentProperty = 6
	propertyStandardQuery          = 0
)

// STORAGE_BUS_TYPE (winioctl.h). El valor 0 es "desconocido" y el resto sigue el
// orden del enumerado de Windows.
const (
	winBusUnknown    = 0
	winBusSCSI       = 1
	winBusATAPI      = 2
	winBusATA        = 3
	winBusIEEE1394   = 4
	winBusSSA        = 5
	winBusFibre      = 6
	winBusUSB        = 7
	winBusRAID       = 8
	winBusiSCSI      = 9
	winBusSAS        = 10
	winBusSATA       = 11
	winBusSD         = 12
	winBusMMC        = 13
	winBusVirtual    = 14
	winBusFileBacked = 15
	winBusSpaces     = 16
	winBusNVMe       = 17
	winBusSCM        = 18
	winBusUFS        = 19
)

// PARTITION_STYLE
const (
	partitionStyleMBR = 0
	partitionStyleGPT = 1
)

// Tamano maximo que acepta ioctlDynamic. Una tabla GPT de 128 entradas con
// nombres ocupa unos 20 KiB; 4 MiB es margen de sobra y evita bucles sin fin.
const maxIoctlBuffer = 4 << 20

// ---------------------------------------------------------------- estructuras

// STORAGE_PROPERTY_QUERY tiene 8 bytes utiles, pero Windows exige el tamano
// completo de la estructura C (12: los dos uint32 mas AdditionalParameters y su
// relleno). Con 8 responde ERROR_BAD_LENGTH, "command length is incorrect".
const (
	storagePropertyQuerySize      = 12
	storagePropertyQueryStructLen = 8
)

// STORAGE_PROPERTY_QUERY
type storagePropertyQuery struct {
	PropertyID uint32
	QueryType  uint32
}

// STORAGE_DEVICE_DESCRIPTOR. El campo RawDeviceProperties y las cadenas van
// detras de esta cabecera, direccionadas por los campos *Offset.
type storageDeviceDescriptor struct {
	Version               uint32
	Size                  uint32
	DeviceType            uint8
	DeviceTypeModifier    uint8
	RemovableMedia        uint8
	CommandQueueing       uint8
	VendorIDOffset        uint32
	ProductIDOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	BusType               uint32
	RawPropertiesLength   uint32
}

// STORAGE_ACCESS_ALIGNMENT_DESCRIPTOR
type storageAccessAlignmentDescriptor struct {
	Version                      uint32
	Size                         uint32
	BytesPerCacheLine            uint32
	BytesOffsetForCacheAlignment uint32
	BytesPerLogicalSector        uint32
	BytesPerPhysicalSector       uint32
}

// DISK_GEOMETRY
type diskGeometry struct {
	Cylinders         int64
	MediaType         uint32
	TracksPerCylinder uint32
	SectorsPerTrack   uint32
	BytesPerSector    uint32
}

// DISK_GEOMETRY_EX. Detras de DiskSize viene un buffer de datos de longitud
// variable que aqui no se usa.
type diskGeometryEx struct {
	Geometry diskGeometry
	DiskSize int64
}

// GET_LENGTH_INFORMATION
type getLengthInformation struct {
	Length int64
}

// DISK_EXTENT
type diskExtent struct {
	DiskNumber     uint32
	_              uint32
	StartingOffset int64
	ExtentLength   int64
}

// Tamano de la cabecera de VOLUME_DISK_EXTENTS: NumberOfDiskExtents mas el
// relleno que alinea Extents a 8 bytes. Los extents empiezan en el byte 8.
const (
	volumeDiskExtentsHeaderSize = 8
	diskExtentSize              = 24
)

// Tamano de la union de PARTITION_INFORMATION_EX. La determina el miembro GPT:
// PartitionType (16) + PartitionId (16) + Attributes (8) + Name[36] (72) = 112.
const (
	partitionUnionSize         = 112
	partitionInformationExSize = 144
)

// PARTITION_INFORMATION_EX
//
// AVISO: el orden real de los campos NO coincide con el que documenta winioctl.h.
// En este Windows la union va al final y los campos de geometria primero. Se
// verifico con datos reales (el GUID de tipo de la ESP cae en el byte 32 de la
// entrada) y queda fijado por test: si un build futuro lo cambia, el test falla.
// Que el tamano cuadre no garantiza el layout: por eso las aserciones de offset.
type partitionInformationEx struct {
	PartitionStyle     uint32
	_                  [4]byte
	StartingOffset     int64
	PartitionLength    int64
	PartitionNumber    uint32
	RewritePartition   uint8
	IsServicePartition uint8
	_                  [2]byte
	Union              [partitionUnionSize]byte
}

// TODO(DG-14): volcar este ABI medido a docs/windisk-abi.md, con la evidencia
// (un disco vacio devuelve 48 bytes) para que nadie lo cambie sin test.
//
// Cabecera de DRIVE_LAYOUT_INFORMATION_EX.
//
// Windows devuelve 48 bytes de cabecera, no los 24 que salen de la
// documentacion: sobran 24 bytes no documentados entre la cabecera y la primera
// entrada. Verificado con un disco vacio (count=0 devuelve exactamente 48) y con
// la distancia entre entradas (144).
const driveLayoutHeaderSize = 48

type driveLayoutInformationEx struct {
	PartitionStyle uint32
	Union          [driveLayoutHeaderSize - 4]byte
}

// Comprobaciones de layout. Si alguna falla, el paquete no compila.
var (
	_ [unsafe.Sizeof(storagePropertyQuery{}) - storagePropertyQueryStructLen]byte
	_ [storagePropertyQueryStructLen - unsafe.Sizeof(storagePropertyQuery{})]byte

	_ [unsafe.Sizeof(storageDeviceDescriptor{}) - 36]byte
	_ [36 - unsafe.Sizeof(storageDeviceDescriptor{})]byte

	_ [unsafe.Sizeof(storageAccessAlignmentDescriptor{}) - 24]byte
	_ [24 - unsafe.Sizeof(storageAccessAlignmentDescriptor{})]byte

	_ [unsafe.Sizeof(diskGeometry{}) - 24]byte
	_ [24 - unsafe.Sizeof(diskGeometry{})]byte

	_ [unsafe.Sizeof(diskGeometryEx{}) - 32]byte
	_ [32 - unsafe.Sizeof(diskGeometryEx{})]byte

	_ [unsafe.Sizeof(getLengthInformation{}) - 8]byte
	_ [8 - unsafe.Sizeof(getLengthInformation{})]byte

	_ [unsafe.Sizeof(diskExtent{}) - diskExtentSize]byte
	_ [diskExtentSize - unsafe.Sizeof(diskExtent{})]byte

	_ [unsafe.Sizeof(partitionInformationEx{}) - partitionInformationExSize]byte
	_ [partitionInformationExSize - unsafe.Sizeof(partitionInformationEx{})]byte

	_ [unsafe.Sizeof(driveLayoutInformationEx{}) - driveLayoutHeaderSize]byte
	_ [driveLayoutHeaderSize - unsafe.Sizeof(driveLayoutInformationEx{})]byte

	// Offsets criticos, medidos sobre datos reales. Si alguno cambia, el paquete
	// no compila.
	_ [unsafe.Offsetof(partitionInformationEx{}.StartingOffset) - 8]byte
	_ [8 - unsafe.Offsetof(partitionInformationEx{}.StartingOffset)]byte
	_ [unsafe.Offsetof(partitionInformationEx{}.PartitionLength) - 16]byte
	_ [16 - unsafe.Offsetof(partitionInformationEx{}.PartitionLength)]byte
	_ [unsafe.Offsetof(partitionInformationEx{}.PartitionNumber) - 24]byte
	_ [24 - unsafe.Offsetof(partitionInformationEx{}.PartitionNumber)]byte
	_ [unsafe.Offsetof(partitionInformationEx{}.Union) - 32]byte
	_ [32 - unsafe.Offsetof(partitionInformationEx{}.Union)]byte
)

// ------------------------------------------------------------------- accesores

// mbr interpreta la union como una particion MBR.
func (p *partitionInformationEx) mbr() (recognized bool, hiddenSectors uint32) {
	return p.Union[0] != 0, binary.LittleEndian.Uint32(p.Union[4:8])
}

// gpt interpreta la union como una particion GPT.
func (p *partitionInformationEx) gpt() (partitionType, partitionID [16]byte, attributes uint64, name string) {
	copy(partitionType[:], p.Union[0:16])
	copy(partitionID[:], p.Union[16:32])
	attributes = binary.LittleEndian.Uint64(p.Union[32:40])

	// Name es un WCHAR[36] en little endian dentro de la union.
	nameBytes := p.Union[40 : 40+72]
	chars := make([]uint16, 0, 36)
	for i := 0; i+1 < len(nameBytes); i += 2 {
		c := binary.LittleEndian.Uint16(nameBytes[i : i+2])
		if c == 0 {
			break
		}
		chars = append(chars, c)
	}
	return partitionType, partitionID, attributes, windows.UTF16ToString(chars)
}

// --------------------------------------------------------------------- helpers

// openDevice abre una ruta de dispositivo con el acceso pedido. Para consultar
// sin privilegios se pasa access 0; para leer se pasa GENERIC_READ.
func openDevice(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("convertir ruta %q: %w", path, err)
	}
	h, err := windows.CreateFile(p, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("abrir %q: %w", path, err)
	}
	return h, nil
}

// ioctl envia un comando con bufferes de entrada y salida fijos y devuelve los
// bytes escritos por el driver.
func ioctl(h windows.Handle, code uint32, in, out []byte) (uint32, error) {
	var inPtr, outPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var returned uint32
	if err := windows.DeviceIoControl(h, code, inPtr, uint32(len(in)),
		outPtr, uint32(len(out)), &returned, nil); err != nil {
		return returned, fmt.Errorf("ioctl 0x%08X: %w", code, err)
	}
	return returned, nil
}

// ioctlDynamic llama a un IOCTL cuyo tamano de salida no se conoce de antemano:
// reintenta duplicando el buffer mientras Windows responda que falta espacio.
func ioctlDynamic(h windows.Handle, code uint32, in []byte, initial int) ([]byte, error) {
	size := initial
	for size <= maxIoctlBuffer {
		out := make([]byte, size)
		n, err := ioctl(h, code, in, out)
		if err == nil {
			if int(n) > len(out) {
				return nil, fmt.Errorf("ioctl 0x%08X devolvio %d bytes en un buffer de %d", code, n, len(out))
			}
			return out[:n], nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) && !errors.Is(err, windows.ERROR_MORE_DATA) {
			return nil, err
		}
		size *= 2
	}
	return nil, fmt.Errorf("ioctl 0x%08X: el buffer supero %d bytes", code, maxIoctlBuffer)
}

// utf16At lee una cadena UTF-16 terminada en NUL que empieza en off dentro de un
// buffer de bytes. Se usa para los nombres de particion GPT.
func utf16At(buf []byte, off uint32) string {
	if off == 0 || int(off)+2 > len(buf) {
		return ""
	}
	chars := make([]uint16, 0, 64)
	for i := int(off); i+1 < len(buf); i += 2 {
		c := binary.LittleEndian.Uint16(buf[i : i+2])
		if c == 0 {
			break
		}
		chars = append(chars, c)
	}
	return windows.UTF16ToString(chars)
}

// asciiAt lee una cadena ASCII terminada en NUL. STORAGE_DEVICE_DESCRIPTOR
// devuelve fabricante, modelo y serial como CHAR, no como WCHAR: decodificarlos
// como UTF-16 produce texto CJK sin sentido. Los bytes altos se tratan como
// Latin-1 para no corromper los que no son ASCII puro.
func asciiAt(buf []byte, off uint32) string {
	if off == 0 || int(off) >= len(buf) {
		return ""
	}
	runes := make([]rune, 0, 64)
	for i := int(off); i < len(buf); i++ {
		if buf[i] == 0 {
			break
		}
		runes = append(runes, rune(buf[i]))
	}
	return string(runes)
}

// guidString formatea un GUID en el orden en que lo guarda Windows, en
// minusculas y sin llaves, que es como lo muestran fdisk y sgdisk.
func guidString(raw [16]byte) string {
	g := windows.GUID{
		Data1: binary.LittleEndian.Uint32(raw[0:4]),
		Data2: binary.LittleEndian.Uint16(raw[4:6]),
		Data3: binary.LittleEndian.Uint16(raw[6:8]),
		Data4: [8]byte(raw[8:16]),
	}
	return strings.ToLower(strings.Trim(g.String(), "{}"))
}

// parseDiskExtents interpreta un buffer de VOLUME_DISK_EXTENTS.
func parseDiskExtents(buf []byte) []diskExtent {
	if len(buf) < volumeDiskExtentsHeaderSize {
		return nil
	}
	n := binary.LittleEndian.Uint32(buf[0:4])
	out := make([]diskExtent, 0, n)
	for i := uint32(0); i < n; i++ {
		off := volumeDiskExtentsHeaderSize + int(i)*diskExtentSize
		if off+diskExtentSize > len(buf) {
			break
		}
		out = append(out, diskExtent{
			DiskNumber:     binary.LittleEndian.Uint32(buf[off : off+4]),
			StartingOffset: int64(binary.LittleEndian.Uint64(buf[off+8 : off+16])),
			ExtentLength:   int64(binary.LittleEndian.Uint64(buf[off+16 : off+24])),
		})
	}
	return out
}

// parseDriveLayout interpreta un buffer de DRIVE_LAYOUT_INFORMATION_EX y
// devuelve el estilo de particionado y las entradas.
//
// El buffer de Windows puede no estar alineado a 8, asi que cada entrada se
// copia a una estructura alineada en lugar de castear el puntero.
func parseDriveLayout(buf []byte) (style uint32, entries []partitionInformationEx, err error) {
	if len(buf) < driveLayoutHeaderSize {
		return 0, nil, fmt.Errorf("tabla de particiones de %d bytes, minimo %d", len(buf), driveLayoutHeaderSize)
	}
	style = binary.LittleEndian.Uint32(buf[0:4])

	// En ambos estilos PartitionCount cae en el byte 4 de la cabecera.
	count := int(binary.LittleEndian.Uint32(buf[4:8]))
	if count <= 0 {
		return style, nil, nil
	}
	if available := (len(buf) - driveLayoutHeaderSize) / partitionInformationExSize; count > available {
		count = available
	}
	if count == 0 {
		return style, nil, nil
	}

	entries = make([]partitionInformationEx, count)
	src := buf[driveLayoutHeaderSize : driveLayoutHeaderSize+count*partitionInformationExSize]
	dst := unsafe.Slice((*byte)(unsafe.Pointer(&entries[0])), len(src))
	copy(dst, src)
	return style, entries, nil
}

// busTypeFromWin32 traduce STORAGE_BUS_TYPE a nuestro modelo.
func busTypeFromWin32(v uint32) blockdev.BusType {
	switch v {
	case winBusUSB:
		return blockdev.BusUSB
	case winBusSATA, winBusATA:
		return blockdev.BusSATA
	case winBusSAS, winBusSCSI:
		// SAS y SCSI no son SATA. Se dejan como desconocidos a proposito: para el
		// guard, "desconocido" nunca cuenta como USB.
		return blockdev.BusUnknown
	case winBusNVMe:
		return blockdev.BusNVMe
	case winBusRAID, winBusiSCSI, winBusFibre, winBusSSA, winBusSCM:
		return blockdev.BusRAID
	case winBusSD, winBusMMC, winBusUFS:
		return blockdev.BusSD
	case winBusVirtual, winBusFileBacked, winBusSpaces:
		return blockdev.BusVirtual
	case winBusIEEE1394, winBusATAPI:
		return blockdev.BusUnknown
	default:
		return blockdev.BusUnknown
	}
}
