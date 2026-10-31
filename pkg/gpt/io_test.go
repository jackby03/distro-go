package gpt

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"path/filepath"
	"testing"

	"distrogo/pkg/blockdev"
)

const testImageSize = 8 << 20 // 8 MiB: sobra para GPT y deja area util

func newTestImage(t *testing.T) *blockdev.Image {
	t.Helper()
	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "test.img"), testImageSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	t.Cleanup(func() { _ = img.Close() })
	return img
}

// buildTable monta la tabla del PoC sobre la imagen indicada.
func buildTable(t *testing.T) (*Table, Layout) {
	t.Helper()
	l, err := NewLayout(testImageSize, blockdev.DefaultSectorSize)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	entries, err := Plan(l, []SizeSpec{
		{Name: "ESP", Type: TypeESP, SizeBytes: 1 << 20},
		{Name: "root", Type: TypeLinuxFilesystem},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	table, err := NewTable(GUID{}, entries)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return table, l
}

func TestProtectiveMBR(t *testing.T) {
	const total = 16 * 1024 * 1024
	mbr := protectiveMBR(total)

	if len(mbr) != mbrSize {
		t.Fatalf("tamano = %d", len(mbr))
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Errorf("falta la firma de arranque: %02x %02x", mbr[510], mbr[511])
	}
	if got := mbr[446+4]; got != protectiveType {
		t.Errorf("tipo de particion = 0x%02X, se esperaba 0xEE", got)
	}
	if got := binary.LittleEndian.Uint32(mbr[446+8:]); got != 1 {
		t.Errorf("LBA de inicio = %d, se esperaba 1", got)
	}
	if got := binary.LittleEndian.Uint32(mbr[446+12:]); got != total-1 {
		t.Errorf("sectores = %d, se esperaban %d", got, total-1)
	}
	// Las otras tres entradas deben estar a cero.
	for i := 1; i < 4; i++ {
		for _, b := range mbr[446+i*16 : 446+i*16+16] {
			if b != 0 {
				t.Fatalf("la entrada %d del MBR no esta a cero", i)
			}
		}
	}
}

func TestProtectiveMBRClampsTo32Bits(t *testing.T) {
	mbr := protectiveMBR(1 << 40)

	if got := binary.LittleEndian.Uint32(mbr[446+12:]); got != 0xFFFFFFFF {
		t.Errorf("sectores = %d, se esperaba el maximo de 32 bits", got)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	disk, err := NewGUID()
	if err != nil {
		t.Fatalf("NewGUID: %v", err)
	}
	in := header{
		MyLBA:             1,
		AlternateLBA:      16383,
		FirstUsable:       34,
		LastUsable:        16350,
		DiskGUID:          disk,
		PartitionEntryLBA: 2,
		NumEntries:        EntryCount,
		EntrySize:         EntrySize,
		EntriesCRC:        0xdeadbeef,
	}

	b := marshalHeader(in)
	out, err := parseHeader(b)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if out != in {
		t.Fatalf("ida y vuelta distinta:\n  in  %+v\n  out %+v", in, out)
	}
}

func TestParseHeaderDetectsCorruption(t *testing.T) {
	disk, _ := NewGUID()
	b := marshalHeader(header{
		MyLBA: 1, AlternateLBA: 16383, FirstUsable: 34, LastUsable: 16350,
		DiskGUID: disk, PartitionEntryLBA: 2, NumEntries: EntryCount, EntrySize: EntrySize,
	})

	corrupted := append([]byte(nil), b...)
	corrupted[60] ^= 0xFF // toca el GUID del disco, que entra en el CRC
	if _, err := parseHeader(corrupted); err == nil {
		t.Fatal("un cambio en el GUID deberia romper el CRC")
	}

	if _, err := parseHeader(b[:40]); err == nil {
		t.Fatal("una cabecera truncada deberia fallar")
	}

	badSig := append([]byte(nil), b...)
	copy(badSig[0:8], "NO PART ")
	if _, err := parseHeader(badSig); err == nil {
		t.Fatal("una firma incorrecta deberia fallar")
	}
}

func TestEntriesRoundTrip(t *testing.T) {
	u1, _ := NewGUID()
	u2, _ := NewGUID()
	parts := []Entry{
		{Type: TypeESP, Unique: u1, FirstLBA: 2048, LastLBA: 4095, Name: "ESP"},
		{Type: TypeLinuxFilesystem, Unique: u2, FirstLBA: 4096, LastLBA: 8191, Name: "root", Attributes: 0x8000000000000000},
	}

	blob, err := marshalEntries(parts)
	if err != nil {
		t.Fatalf("marshalEntries: %v", err)
	}
	if len(blob) != EntryCount*EntrySize {
		t.Fatalf("tamano del array = %d", len(blob))
	}

	got, err := parseEntries(blob, EntryCount, EntrySize)
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("entradas = %d, se esperaban 2", len(got))
	}
	for i := range parts {
		if got[i] != parts[i] {
			t.Errorf("entrada %d:\n  in  %+v\n  out %+v", i, parts[i], got[i])
		}
	}
}

func TestEntriesNameLimit(t *testing.T) {
	u, _ := NewGUID()
	parts := []Entry{{
		Type: TypeESP, Unique: u, FirstLBA: 2048, LastLBA: 4095,
		Name: "0123456789012345678901234567890123456", // 37 caracteres
	}}
	if _, err := marshalEntries(parts); err == nil {
		t.Fatal("un nombre de 37 caracteres deberia fallar")
	}
}

func TestCommitAndRead(t *testing.T) {
	img := newTestImage(t)
	table, l := buildTable(t)

	if err := Commit(img, table, l); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, gotLayout, err := Read(img)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.DiskGUID != table.DiskGUID {
		t.Errorf("GUID del disco = %s, se esperaba %s", got.DiskGUID, table.DiskGUID)
	}
	if len(got.Partitions) != len(table.Partitions) {
		t.Fatalf("particiones = %d, se esperaban %d", len(got.Partitions), len(table.Partitions))
	}
	for i := range table.Partitions {
		if got.Partitions[i] != table.Partitions[i] {
			t.Errorf("particion %d:\n  in  %+v\n  out %+v", i, table.Partitions[i], got.Partitions[i])
		}
	}
	if gotLayout.FirstUsable != l.FirstUsable || gotLayout.LastUsable != l.LastUsable {
		t.Errorf("layout leido %+v, se esperaba first=%d last=%d", gotLayout, l.FirstUsable, l.LastUsable)
	}
}

func TestCommitWritesProtectiveMBR(t *testing.T) {
	img := newTestImage(t)
	table, l := buildTable(t)

	if err := Commit(img, table, l); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	mbr := make([]byte, mbrSize)
	if _, err := img.ReadAt(mbr, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if mbr[446+4] != protectiveType {
		t.Errorf("el MBR protector no esta en el sector 0")
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Error("falta la firma de arranque")
	}
}

func TestCommitWritesBackupTable(t *testing.T) {
	img := newTestImage(t)
	table, l := buildTable(t)

	if err := Commit(img, table, l); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	sector := make([]byte, l.SectorSize)
	if _, err := img.ReadAt(sector, int64(l.TotalSectors-1)*int64(l.SectorSize)); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	backup, err := parseHeader(sector)
	if err != nil {
		t.Fatalf("la cabecera de respaldo no es valida: %v", err)
	}
	if backup.MyLBA != l.TotalSectors-1 {
		t.Errorf("MyLBA de respaldo = %d", backup.MyLBA)
	}
	if backup.AlternateLBA != 1 {
		t.Errorf("AlternateLBA de respaldo = %d, se esperaba 1", backup.AlternateLBA)
	}
	if backup.PartitionEntryLBA != l.TotalSectors-1-l.EntrySectors {
		t.Errorf("las entradas de respaldo apuntan a %d", backup.PartitionEntryLBA)
	}
}

func TestCommitIsIdempotent(t *testing.T) {
	img := newTestImage(t)
	table, l := buildTable(t)

	if err := Commit(img, table, l); err != nil {
		t.Fatalf("primer Commit: %v", err)
	}
	first := make([]byte, 2*l.SectorSize)
	if _, err := img.ReadAt(first, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}

	if err := Commit(img, table, l); err != nil {
		t.Fatalf("segundo Commit: %v", err)
	}
	second := make([]byte, 2*l.SectorSize)
	if _, err := img.ReadAt(second, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}

	if string(first) != string(second) {
		t.Fatal("dos commits de la misma tabla deberian dejar los mismos bytes")
	}
}

// Una tabla invalida no debe escribir nada: el disco tiene que quedar intacto.
func TestCommitRejectsInvalidTableWithoutWriting(t *testing.T) {
	img := newTestImage(t)
	table, l := buildTable(t)
	table.Partitions[1].FirstLBA = table.Partitions[0].FirstLBA // solape

	err := Commit(img, table, l)
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("error = %v, se esperaba ErrOverlap", err)
	}

	head := make([]byte, 2*l.SectorSize)
	if _, err := img.ReadAt(head, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	for i, b := range head {
		if b != 0 {
			t.Fatalf("el disco no deberia haberse tocado; byte %d = 0x%02X", i, b)
		}
	}
}

func TestReadDetectsCorruptedEntries(t *testing.T) {
	img := newTestImage(t)
	table, l := buildTable(t)
	if err := Commit(img, table, l); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Tocar el nombre de la primera entrada invalida el CRC del array.
	off := int64(2)*int64(l.SectorSize) + 56
	if _, err := img.WriteAt([]byte{0xFF}, off); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}

	if _, _, err := Read(img); err == nil {
		t.Fatal("Read deberia detectar el CRC de entradas roto")
	}
}

func TestReadRejectsEmptyDisk(t *testing.T) {
	img := newTestImage(t)

	if _, _, err := Read(img); err == nil {
		t.Fatal("una imagen a cero no tiene tabla y deberia fallar")
	}
}

func TestEntriesCRCIsOverDeclaredSize(t *testing.T) {
	// El CRC se calcula sobre NumEntries*EntrySize, no sobre el relleno a sector.
	blob, err := marshalEntries(nil)
	if err != nil {
		t.Fatalf("marshalEntries: %v", err)
	}
	if got, want := crc32.ChecksumIEEE(blob), crc32.ChecksumIEEE(make([]byte, EntryCount*EntrySize)); got != want {
		t.Errorf("CRC = 0x%08X, se esperaba 0x%08X", got, want)
	}
}

// Contraste con un GPT ajeno: el ISO de Mint es hibrido y lleva una tabla
// generada por xorriso, no por nosotros ni por Windows. Si nuestro parser la
// entiende, el formato esta bien entendido. Se salta si falta la fixture.
func TestReadRealGPTFromMintISO(t *testing.T) {
	const iso = "../../testdata/isos/linuxmint-22.3-xfce-64bit.iso"
	img, err := blockdev.OpenImage(iso, false)
	if err != nil {
		t.Skipf("fixture no disponible: %v", err)
	}
	defer img.Close()

	table, layout, err := Read(img)
	if err != nil {
		t.Fatalf("Read sobre un GPT ajeno: %v", err)
	}
	if len(table.Partitions) == 0 {
		t.Fatal("el ISO deberia declarar al menos una particion")
	}
	t.Logf("GUID del disco: %s, area util %d..%d, %d particiones",
		table.DiskGUID, layout.FirstUsable, layout.LastUsable, len(table.Partitions))
	for i, p := range table.Partitions {
		t.Logf("  %d: tipo %s  LBA %d..%d  %q", i+1, p.Type, p.FirstLBA, p.LastLBA, p.Name)
	}
}
