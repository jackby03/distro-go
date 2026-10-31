//go:build windows

package windisk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"

	"distrogo/pkg/blockdev"
)

// TODO(DG-15): este fichero pasa de 470 lineas y mezcla apertura y consultas de
// disco con enumeracion de unidades y mapeo de volumenes. Partirlo.
const (
	// maxDisks es hasta donde se sondea la numeracion de discos. Windows no
	// ofrece una lista de discos fisicos: hay que abrir \\.\PhysicalDriveN y
	// parar cuando dejan de existir.
	maxDisks = 32
	// maxGaps son los numeros consecutivos inexistentes que se toleran antes de
	// dejar de sondear. Un disco offline deja un hueco en la numeracion.
	maxGaps = 8
)

// Enumerate devuelve el inventario de discos fisicos de la maquina.
//
// Solo usa IOCTL de consulta y no necesita elevacion: abre cada disco con
// GENERIC_READ y, si Windows lo niega, reintenta con acceso 0 y anota en
// Warnings lo que no pudo leer.
//
// IsBoot e IsSystem se marcan en el disco que aloja el volumen de %SystemRoot%.
// Windows distingue "disco del sistema" (donde esta el gestor de arranque) de
// "disco de arranque" (donde esta Windows), pero esa distincion requiere leer la
// BCD. Aqui el sesgo es proteger de mas: ambos indican lo mismo.
func Enumerate() (Inventory, error) {
	var inv Inventory

	volumes := enumerateVolumes(&inv)

	gaps := 0
	for n := uint32(0); n < maxDisks; n++ {
		d, found, err := readDisk(n, &inv)
		if err != nil {
			return inv, err
		}
		if !found {
			gaps++
			if gaps >= maxGaps {
				break
			}
			continue
		}
		gaps = 0
		attachVolumes(&d, volumes, &inv)
		inv.Disks = append(inv.Disks, d)
	}

	markSystemDisk(&inv)
	return inv, nil
}

// readDisk abre y consulta un disco. found es false cuando el numero de disco no
// existe, que es la senal para dejar de sondear.
func readDisk(number uint32, inv *Inventory) (d blockdev.Disk, found bool, err error) {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, number)

	h, err := openWithFallback(path)
	if err != nil {
		if isMissingDevice(err) {
			return blockdev.Disk{}, false, nil
		}
		return blockdev.Disk{}, false, fmt.Errorf("disco %d: %w", number, err)
	}
	if h == windows.InvalidHandle {
		inv.warn("disco %d: sin acceso para consultarlo", number)
		return blockdev.Disk{}, false, nil
	}
	// Un handle por llamada: el defer se ejecuta al volver de readDisk, no se
	// acumula entre discos.
	defer windows.CloseHandle(h)

	d = blockdev.Disk{Number: number}

	if desc, derr := queryDescriptor(h); derr != nil {
		inv.warn("disco %d: descriptor: %v", number, derr)
	} else {
		d.Model = desc.model
		d.Serial = desc.serial
		d.Bus = busTypeFromWin32(desc.busType)
		d.Removable = desc.removable
	}

	size, logical, gerr := queryGeometry(h)
	if gerr != nil {
		inv.warn("disco %d: geometria: %v", number, gerr)
	}
	d.SizeBytes = size
	d.LogicalSector = logical

	if align, aerr := queryAlignment(h); aerr != nil {
		inv.warn("disco %d: alineacion: %v", number, aerr)
	} else {
		if align.logical > 0 {
			d.LogicalSector = align.logical
		}
		d.PhysicalSector = align.physical
	}
	if d.LogicalSector == 0 {
		d.LogicalSector = blockdev.DefaultSectorSize
	}
	if d.PhysicalSector == 0 {
		d.PhysicalSector = d.LogicalSector
	}

	if ro, rerr := queryReadOnly(h); rerr != nil {
		inv.warn("disco %d: estado de escritura: %v", number, rerr)
	} else {
		d.ReadOnly = ro
	}

	parts, perr := queryLayout(h)
	if perr != nil {
		inv.warn("disco %d: tabla de particiones: %v", number, perr)
	} else {
		d.Partitions = parts
	}

	return d, true, nil
}

// --------------------------------------------------------------- descriptores

type deviceDescriptor struct {
	model     string
	serial    string
	busType   uint32
	removable bool
}

func queryDescriptor(h windows.Handle) (deviceDescriptor, error) {
	in := make([]byte, storagePropertyQuerySize)
	binary.LittleEndian.PutUint32(in[0:4], storageDeviceProperty)
	binary.LittleEndian.PutUint32(in[4:8], propertyStandardQuery)

	buf, err := ioctlDynamic(h, ioctlStorageQueryProperty, in, 512)
	if err != nil {
		return deviceDescriptor{}, err
	}
	return parseDeviceDescriptor(buf)
}

// parseDeviceDescriptor interpreta un STORAGE_DEVICE_DESCRIPTOR. Las cadenas van
// empaquetadas detras de la cabecera y se localizan por offsets.
func parseDeviceDescriptor(buf []byte) (deviceDescriptor, error) {
	const headerSize = 36
	if len(buf) < headerSize {
		return deviceDescriptor{}, fmt.Errorf("descriptor de %d bytes, minimo %d", len(buf), headerSize)
	}
	vendor := strings.TrimSpace(asciiAt(buf, binary.LittleEndian.Uint32(buf[12:16])))
	product := strings.TrimSpace(asciiAt(buf, binary.LittleEndian.Uint32(buf[16:20])))
	return deviceDescriptor{
		model:     strings.TrimSpace(vendor + " " + product),
		serial:    strings.TrimSpace(asciiAt(buf, binary.LittleEndian.Uint32(buf[24:28]))),
		busType:   binary.LittleEndian.Uint32(buf[28:32]),
		removable: buf[10] != 0,
	}, nil
}

type alignmentInfo struct {
	logical  int
	physical int
}

func queryAlignment(h windows.Handle) (alignmentInfo, error) {
	in := make([]byte, storagePropertyQuerySize)
	binary.LittleEndian.PutUint32(in[0:4], storageAccessAlignmentProperty)
	binary.LittleEndian.PutUint32(in[4:8], propertyStandardQuery)

	buf, err := ioctlDynamic(h, ioctlStorageQueryProperty, in, 64)
	if err != nil {
		return alignmentInfo{}, err
	}
	return parseAlignment(buf)
}

func parseAlignment(buf []byte) (alignmentInfo, error) {
	const headerSize = 24
	if len(buf) < headerSize {
		return alignmentInfo{}, fmt.Errorf("alineacion de %d bytes, minimo %d", len(buf), headerSize)
	}
	return alignmentInfo{
		logical:  int(binary.LittleEndian.Uint32(buf[16:20])),
		physical: int(binary.LittleEndian.Uint32(buf[20:24])),
	}, nil
}

// ------------------------------------------------------------------ geometria

func queryGeometry(h windows.Handle) (size int64, logicalSector int, err error) {
	buf, err := ioctlDynamic(h, ioctlDiskGetDriveGeometryEx, nil, 64)
	if err != nil {
		return 0, 0, err
	}
	return parseGeometry(buf)
}

// parseGeometry interpreta un DISK_GEOMETRY_EX: DISK_GEOMETRY son los 24
// primeros bytes y DiskSize viene justo despues.
func parseGeometry(buf []byte) (size int64, logicalSector int, err error) {
	const headerSize = 32
	if len(buf) < headerSize {
		return 0, 0, fmt.Errorf("geometria de %d bytes, minimo %d", len(buf), headerSize)
	}
	logicalSector = int(binary.LittleEndian.Uint32(buf[20:24]))
	size = int64(binary.LittleEndian.Uint64(buf[24:32]))
	return size, logicalSector, nil
}

// --------------------------------------------------------------- particiones

func queryLayout(h windows.Handle) ([]blockdev.Partition, error) {
	buf, err := ioctlDynamic(h, ioctlDiskGetDriveLayoutEx, nil, 4096)
	if err != nil {
		return nil, err
	}
	_, entries, err := parseDriveLayout(buf)
	if err != nil {
		return nil, err
	}

	parts := make([]blockdev.Partition, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		if e.PartitionLength <= 0 {
			continue
		}
		p := blockdev.Partition{
			Number:    e.PartitionNumber,
			Offset:    e.StartingOffset,
			SizeBytes: e.PartitionLength,
		}
		if e.PartitionStyle == partitionStyleGPT {
			partitionType, _, _, _ := e.gpt()
			p.TypeGUID = guidString(partitionType)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func queryReadOnly(h windows.Handle) (bool, error) {
	_, err := ioctl(h, ioctlDiskIsWritable, nil, nil)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, windows.ERROR_WRITE_PROTECT):
		return true, nil
	default:
		return false, err
	}
}

// ------------------------------------------------------------------- volumenes

// enumerateVolumes recorre las letras de unidad y devuelve donde vive cada
// volumen. La consulta se hace con acceso 0 y funciona sin elevacion.
func enumerateVolumes(inv *Inventory) []blockdev.Volume {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		inv.warn("no se pudieron enumerar las unidades: %v", err)
		return nil
	}

	var out []blockdev.Volume
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := byte('A' + i)
		if !isDataVolume(letter) {
			continue
		}
		out = append(out, volumesForLetter(letter, inv)...)
	}
	return out
}

// Tipos de unidad de GetDriveType.
const (
	driveRemovable = 2
	driveFixed     = 3
)

// isDataVolume filtra unidades que no aportan informacion de disco: CD-ROM,
// unidades de red, ramdisk y raices inexistentes. Sin este filtro, un lector
// optico genera un aviso por cada enumeracion.
func isDataVolume(letter byte) bool {
	p, err := windows.UTF16PtrFromString(string([]byte{letter, ':', '\\'}))
	if err != nil {
		return false
	}
	switch windows.GetDriveType(p) {
	case driveFixed, driveRemovable:
		return true
	default:
		return false
	}
}

func volumesForLetter(letter byte, inv *Inventory) []blockdev.Volume {
	root := string([]byte{letter, ':', '\\'})

	h, err := openWithFallback(fmt.Sprintf(`\\.\%c:`, letter))
	if err != nil {
		inv.warn("unidad %s: %v", root, err)
		return nil
	}
	if h == windows.InvalidHandle {
		inv.warn("unidad %s: sin acceso", root)
		return nil
	}
	// Un handle por unidad: el defer se ejecuta al volver de esta funcion.
	defer windows.CloseHandle(h)

	base := blockdev.Volume{MountPoint: root}
	base.Label, base.FileSystem = volumeLabelAndFS(h)
	if guid, gerr := volumeGUID(root); gerr == nil {
		base.GUID = guid
	}

	extents, err := queryDiskExtents(h)
	if err != nil {
		// Algunos volumenes no se corresponden con ningun disco fisico: unidades
		// subst, volumentes virtuales. No es un error, simplemente no aportan.
		if !errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
			inv.warn("unidad %s: extensiones de disco: %v", root, err)
		}
		return nil
	}

	out := make([]blockdev.Volume, 0, len(extents))
	for _, e := range extents {
		v := base
		v.DiskNumber = e.DiskNumber
		v.Offset = e.StartingOffset
		out = append(out, v)
	}
	return out
}

func queryDiskExtents(h windows.Handle) ([]diskExtent, error) {
	buf, err := ioctlDynamic(h, ioctlVolumeGetVolumeDiskExtents, nil, 64)
	if err != nil {
		return nil, err
	}
	return parseDiskExtents(buf), nil
}

// TODO(DG-22): con acceso 0 esta consulta falla en algunas unidades (D: sale sin
// sistema de ficheros). Reintentar con GENERIC_READ cuando el handle lo permita.
func volumeLabelAndFS(h windows.Handle) (label, fs string) {
	labelBuf := make([]uint16, 261)
	fsBuf := make([]uint16, 64)
	var serial, maxComponent, flags uint32

	err := windows.GetVolumeInformationByHandle(h, &labelBuf[0], uint32(len(labelBuf)),
		&serial, &maxComponent, &flags, &fsBuf[0], uint32(len(fsBuf)))
	if err != nil {
		return "", ""
	}
	return windows.UTF16ToString(labelBuf), windows.UTF16ToString(fsBuf)
}

// volumeGUID devuelve el GUID del volumen a partir de su punto de montaje, en la
// forma que usa Windows internamente: \\?\Volume{...}\
func volumeGUID(root string) (string, error) {
	rootPtr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 261)
	if err := windows.GetVolumeNameForVolumeMountPoint(rootPtr, &buf[0], uint32(len(buf))); err != nil {
		return "", err
	}
	s := windows.UTF16ToString(buf)
	if open := strings.Index(s, "{"); open >= 0 {
		if close := strings.Index(s[open:], "}"); close > 0 {
			return s[open+1 : open+close], nil
		}
	}
	return strings.TrimSuffix(s, `\`), nil
}

// attachVolumes asocia cada volumen del disco a la particion en la que empieza.
func attachVolumes(d *blockdev.Disk, volumes []blockdev.Volume, inv *Inventory) {
	for i := range volumes {
		v := volumes[i]
		if v.DiskNumber != d.Number {
			continue
		}
		attached := false
		for j := range d.Partitions {
			if d.Partitions[j].Offset == v.Offset {
				vc := v
				d.Partitions[j].Volume = &vc
				attached = true
				break
			}
		}
		if !attached {
			inv.warn("disco %d: el volumen %s (offset %d) no empieza en ninguna particion conocida",
				d.Number, v.MountPoint, v.Offset)
		}
	}
}

// TODO(DG-17): IsSystem e IsBoot son aproximados: ambos se marcan en el disco
// de %SystemRoot%. La distincion real entre disco de sistema y de arranque sale
// de la BCD.
//
// markSystemDisk marca el disco que aloja %SystemRoot%.
func markSystemDisk(inv *Inventory) {
	number, ok := systemRootDisk(inv)
	if !ok {
		inv.warn("no se pudo localizar el disco de %%SystemRoot%%")
		return
	}
	for i := range inv.Disks {
		if inv.Disks[i].Number == number {
			inv.Disks[i].IsBoot = true
			inv.Disks[i].IsSystem = true
		}
	}
}

func systemRootDisk(inv *Inventory) (uint32, bool) {
	root := os.Getenv("SystemRoot")
	if len(root) < 2 || root[1] != ':' {
		return 0, false
	}
	drive := strings.ToUpper(root[:2])

	for _, d := range inv.Disks {
		for _, p := range d.Partitions {
			if p.Volume == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSuffix(p.Volume.MountPoint, `\`), drive) {
				return d.Number, true
			}
		}
	}
	return 0, false
}

// isMissingDevice indica que el numero de disco no existe.
func isMissingDevice(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_INVALID_NAME)
}

// TODO(DG-19): la rama GENERIC_READ de openWithFallback no se ejercita en una
// consola sin privilegios. Verificarla una vez como administrador.
//
// openWithFallback intenta abrir con GENERIC_READ, que da acceso a todos los
// IOCTL de consulta. Si Windows lo niega por falta de privilegios, reintenta con
// acceso 0: se pierden algunos datos, pero se puede enumerar sin ser admin.
//
// Devuelve InvalidHandle y un error solo si tampoco se pudo abrir con acceso 0.
func openWithFallback(path string) (windows.Handle, error) {
	h, err := openDevice(path, windows.GENERIC_READ)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return windows.InvalidHandle, err
	}
	return openDevice(path, 0)
}
