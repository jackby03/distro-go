//go:build windows

package windisk

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// appendUTF16 anade una cadena UTF-16 terminada en NUL y devuelve su offset.
// Sirve para fabricar los buffers que Windows devuelve empaquetados.
func appendASCII(dst []byte, s string) ([]byte, uint32) {
	off := uint32(len(dst))
	dst = append(dst, s...)
	dst = append(dst, 0)
	return dst, off
}

func TestParseDeviceDescriptor(t *testing.T) {
	buf := make([]byte, 36)
	buf[10] = 1 // RemovableMedia
	var vendorOff, productOff, serialOff uint32
	buf, vendorOff = appendASCII(buf, "SanDisk")
	buf, productOff = appendASCII(buf, "Extreme SSD")
	buf, serialOff = appendASCII(buf, "201703310007F")

	binary.LittleEndian.PutUint32(buf[0:4], 1) // Version
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf)))
	binary.LittleEndian.PutUint32(buf[12:16], vendorOff)
	binary.LittleEndian.PutUint32(buf[16:20], productOff)
	binary.LittleEndian.PutUint32(buf[24:28], serialOff)
	binary.LittleEndian.PutUint32(buf[28:32], winBusUSB)

	desc, err := parseDeviceDescriptor(buf)
	if err != nil {
		t.Fatalf("parseDeviceDescriptor: %v", err)
	}
	if desc.model != "SanDisk Extreme SSD" {
		t.Errorf("model = %q", desc.model)
	}
	if desc.serial != "201703310007F" {
		t.Errorf("serial = %q", desc.serial)
	}
	if !desc.removable {
		t.Error("se esperaba removable = true")
	}
	if got := busTypeFromWin32(desc.busType).String(); got != "usb" {
		t.Errorf("bus = %v, se esperaba usb", got)
	}
}

func TestParseDeviceDescriptorTooShort(t *testing.T) {
	if _, err := parseDeviceDescriptor(make([]byte, 20)); err == nil {
		t.Fatal("un descriptor truncado deberia dar error")
	}
}

func TestParseGeometry(t *testing.T) {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint32(buf[20:24], 512)
	binary.LittleEndian.PutUint64(buf[24:32], 500107862016)

	size, logical, err := parseGeometry(buf)
	if err != nil {
		t.Fatalf("parseGeometry: %v", err)
	}
	if size != 500107862016 {
		t.Errorf("size = %d", size)
	}
	if logical != 512 {
		t.Errorf("logicalSector = %d", logical)
	}
}

func TestParseAlignment(t *testing.T) {
	buf := make([]byte, 24)
	binary.LittleEndian.PutUint32(buf[16:20], 512)
	binary.LittleEndian.PutUint32(buf[20:24], 4096)

	align, err := parseAlignment(buf)
	if err != nil {
		t.Fatalf("parseAlignment: %v", err)
	}
	if align.logical != 512 || align.physical != 4096 {
		t.Errorf("alineacion = %+v", align)
	}
}

func TestParseDriveLayoutGoldenEntry(t *testing.T) {
	// Entrada real capturada de \.\PhysicalDrive1 (Windows 11 26300): la ESP.
	// 144 bytes exactos, empezando en el byte 48 del buffer que devuelve Windows.
	// Si el ABI cambia, este test falla y no adivinamos.
	const entryHex = "" +
		"0100000000000000" + // +0  PartitionStyle = 1 (GPT)
		"0000100000000000" + // +8  StartingOffset = 1048576
		"0000400600000000" + // +16 PartitionLength = 104857600
		"0100000000000000" + // +24 PartitionNumber = 1
		"28732ac11ff8d211" + // +32 PartitionType = C12A7328-F81F-11D2-BA4B-00A0C93EC93B
		"ba4b00a0c93ec93b" +
		"b1fa8c28e920cc46" + // +48 PartitionId
		"a1906bfb09be4ba5" +
		"000000000000 0080" + // +64 Attributes = 0x8000000000000000
		"4500460049002000" + // +72 Name = "EFI system partition"
		"7300790073007400" +
		"65006d0020007000" +
		"6100720074006900" +
		"740069006f006e00"

	entry, err := hex.DecodeString(strings.ReplaceAll(entryHex, " ", ""))
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	entry = append(entry, make([]byte, partitionInformationExSize-len(entry))...)

	buf := make([]byte, driveLayoutHeaderSize+partitionInformationExSize)
	binary.LittleEndian.PutUint32(buf[0:4], partitionStyleGPT)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	copy(buf[driveLayoutHeaderSize:], entry)

	style, entries, err := parseDriveLayout(buf)
	if err != nil {
		t.Fatalf("parseDriveLayout: %v", err)
	}
	if style != partitionStyleGPT {
		t.Errorf("style = %d", style)
	}
	if len(entries) != 1 {
		t.Fatalf("entradas = %d, se esperaba 1", len(entries))
	}

	got := entries[0]
	if got.StartingOffset != 1048576 {
		t.Errorf("StartingOffset = %d", got.StartingOffset)
	}
	if got.PartitionLength != 104857600 {
		t.Errorf("PartitionLength = %d", got.PartitionLength)
	}
	if got.PartitionNumber != 1 {
		t.Errorf("PartitionNumber = %d", got.PartitionNumber)
	}

	partitionType, _, attributes, name := got.gpt()
	if guidString(partitionType) != "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" {
		t.Errorf("PartitionType = %s", guidString(partitionType))
	}
	if attributes != 0x8000000000000000 {
		t.Errorf("Attributes = 0x%016X", attributes)
	}
	if name != "EFI system partition" {
		t.Errorf("Name = %q", name)
	}
}

func TestParseDriveLayoutRejectsShortBuffer(t *testing.T) {
	if _, _, err := parseDriveLayout(make([]byte, 8)); err == nil {
		t.Fatal("un buffer demasiado corto deberia dar error")
	}
}

func TestParseDiskExtents(t *testing.T) {
	buf := make([]byte, volumeDiskExtentsHeaderSize+2*diskExtentSize)
	binary.LittleEndian.PutUint32(buf[0:4], 2)

	binary.LittleEndian.PutUint32(buf[8:12], 3)     // DiskNumber
	binary.LittleEndian.PutUint64(buf[16:24], 4096) // StartingOffset
	binary.LittleEndian.PutUint64(buf[24:32], 1024) // ExtentLength

	second := volumeDiskExtentsHeaderSize + diskExtentSize
	binary.LittleEndian.PutUint32(buf[second:second+4], 2)
	binary.LittleEndian.PutUint64(buf[second+8:second+16], 8192)

	extents := parseDiskExtents(buf)
	if len(extents) != 2 {
		t.Fatalf("extents = %d, se esperaban 2", len(extents))
	}
	if extents[0].DiskNumber != 3 || extents[0].StartingOffset != 4096 || extents[0].ExtentLength != 1024 {
		t.Errorf("extent 0 = %+v", extents[0])
	}
	if extents[1].DiskNumber != 2 || extents[1].StartingOffset != 8192 {
		t.Errorf("extent 1 = %+v", extents[1])
	}
}

func TestParseDiskExtentsHandlesTruncatedBuffer(t *testing.T) {
	buf := make([]byte, 8+diskExtentSize) // dice 5 extents pero solo cabe 1
	binary.LittleEndian.PutUint32(buf[0:4], 5)
	binary.LittleEndian.PutUint32(buf[8:12], 7)

	extents := parseDiskExtents(buf)
	if len(extents) != 1 {
		t.Fatalf("extents = %d, se esperaba 1 (el resto no cabe)", len(extents))
	}
}

func TestBusTypeFromWin32(t *testing.T) {
	cases := []struct {
		in   uint32
		want string
	}{
		{winBusUSB, "usb"},
		{winBusSATA, "sata"},
		{winBusATA, "sata"},
		{winBusNVMe, "nvme"},
		{winBusRAID, "raid"},
		{winBusVirtual, "virtual"},
		{winBusSD, "sd"},
		{winBusSCSI, "desconocido"},
		{winBusSAS, "desconocido"},
		{9999, "desconocido"},
	}
	for _, c := range cases {
		if got := busTypeFromWin32(c.in).String(); got != c.want {
			t.Errorf("busTypeFromWin32(%d) = %q, se esperaba %q", c.in, got, c.want)
		}
	}
}

// TestEnumerateThisMachine es la prueba de aceptacion de M1: recorre los discos
// reales del equipo. Con `go test -v` imprime el inventario, que es exactamente
// lo que mostrara `distrogo list`.
func TestEnumerateThisMachine(t *testing.T) {
	inv, err := Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for _, w := range inv.Warnings {
		t.Logf("aviso: %v", w)
	}

	if len(inv.Disks) == 0 {
		t.Fatal("no se encontro ningun disco")
	}

	for _, d := range inv.Disks {
		t.Logf("disco %d: %s | bus %s | serial %q | %.1f GiB | sector logico %d fisico %d | removable %v | readonly %v | system %v",
			d.Number, d.Model, d.Bus, d.Serial,
			float64(d.SizeBytes)/(1<<30), d.LogicalSector, d.PhysicalSector,
			d.Removable, d.ReadOnly, d.IsSystem)

		for _, p := range d.Partitions {
			mount := ""
			if p.Volume != nil {
				mount = p.Volume.MountPoint
			}
			t.Logf("    particion %d: offset %d, %.1f GiB, tipo %s, montada en %s",
				p.Number, p.Offset, float64(p.SizeBytes)/(1<<30), p.TypeGUID, mount)
		}

		if d.SizeBytes <= 0 {
			t.Errorf("disco %d: tamano %d", d.Number, d.SizeBytes)
		}
		if d.LogicalSector < 512 {
			t.Errorf("disco %d: sector logico %d", d.Number, d.LogicalSector)
		}
		if d.PhysicalSector < d.LogicalSector {
			t.Errorf("disco %d: sector fisico %d menor que el logico %d",
				d.Number, d.PhysicalSector, d.LogicalSector)
		}
	}

	// El disco que aloja %SystemRoot% debe estar identificado y marcado.
	root := os.Getenv("SystemRoot")
	drive := strings.ToUpper(root[:2])
	var systemDisk *blockdevDiskRef
	for i := range inv.Disks {
		for _, p := range inv.Disks[i].Partitions {
			if p.Volume == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSuffix(p.Volume.MountPoint, `\`), drive) {
				systemDisk = &blockdevDiskRef{number: inv.Disks[i].Number, isBoot: inv.Disks[i].IsBoot}
			}
		}
	}
	if systemDisk == nil {
		t.Fatalf("no se encontro el volumen de %s en el inventario", drive)
	}
	if !systemDisk.isBoot {
		t.Errorf("el disco %d aloja %s pero no esta marcado como IsBoot", systemDisk.number, drive)
	}
	t.Logf("disco del sistema: %d", systemDisk.number)
}

type blockdevDiskRef struct {
	number uint32
	isBoot bool
}
