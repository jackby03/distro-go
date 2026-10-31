package gpt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"unicode/utf16"

	"distrogo/pkg/blockdev"
)

const (
	// signature es el magic de la cabecera GPT.
	signature = "EFI PART"
	// revision es la 1.0, la unica que existe.
	revision = 0x00010000
	// protectiveType es el tipo de particion del MBR protector.
	protectiveType = 0xEE
	// mbrSize es el tamano del MBR dentro del primer sector, sea cual sea el
	// tamano de sector del disco.
	mbrSize = 512
)

// protectiveMBR construye el MBR que oculta el disco a las herramientas que solo
// entienden MBR. Deja el disco marcado como "GPT" con una unica particion de
// tipo 0xEE que ocupa todo.
func protectiveMBR(totalSectors uint64) []byte {
	mbr := make([]byte, mbrSize)

	entry := mbr[446:462]
	entry[0] = 0x00                                 // no arrancable
	entry[1], entry[2], entry[3] = 0x00, 0x02, 0x00 // CHS de inicio, convencion
	entry[4] = protectiveType
	entry[5], entry[6], entry[7] = 0xFF, 0xFF, 0xFF // CHS de fin, "demasiado grande"
	binary.LittleEndian.PutUint32(entry[8:12], 1)

	size := totalSectors
	if size > 0 {
		size--
	}
	if size > 0xFFFFFFFF {
		size = 0xFFFFFFFF
	}
	binary.LittleEndian.PutUint32(entry[12:16], uint32(size))

	mbr[510], mbr[511] = 0x55, 0xAA
	return mbr
}

// header es la cabecera GPT antes de serializar.
type header struct {
	MyLBA             uint64
	AlternateLBA      uint64
	FirstUsable       uint64
	LastUsable        uint64
	DiskGUID          GUID
	PartitionEntryLBA uint64
	NumEntries        uint32
	EntrySize         uint32
	EntriesCRC        uint32
}

// marshalHeader serializa la cabecera y calcula su CRC. El CRC se calcula con el
// campo a cero, que es como lo verifica todo el mundo.
func marshalHeader(h header) []byte {
	b := make([]byte, HeaderSize)
	copy(b[0:8], signature)
	binary.LittleEndian.PutUint32(b[8:12], revision)
	binary.LittleEndian.PutUint32(b[12:16], HeaderSize)
	binary.LittleEndian.PutUint32(b[20:24], 0)
	binary.LittleEndian.PutUint64(b[24:32], h.MyLBA)
	binary.LittleEndian.PutUint64(b[32:40], h.AlternateLBA)
	binary.LittleEndian.PutUint64(b[40:48], h.FirstUsable)
	binary.LittleEndian.PutUint64(b[48:56], h.LastUsable)
	copy(b[56:72], h.DiskGUID[:])
	binary.LittleEndian.PutUint64(b[72:80], h.PartitionEntryLBA)
	binary.LittleEndian.PutUint32(b[80:84], h.NumEntries)
	binary.LittleEndian.PutUint32(b[84:88], h.EntrySize)
	binary.LittleEndian.PutUint32(b[88:92], h.EntriesCRC)
	binary.LittleEndian.PutUint32(b[16:20], crc32.ChecksumIEEE(b))
	return b
}

// parseHeader valida la firma, la revision y el CRC de una cabecera.
func parseHeader(b []byte) (header, error) {
	var h header
	if len(b) < HeaderSize {
		return h, fmt.Errorf("cabecera GPT de %d bytes, minimo %d", len(b), HeaderSize)
	}
	if !bytes.Equal(b[0:8], []byte(signature)) {
		return h, fmt.Errorf("firma GPT incorrecta: %q", b[0:8])
	}
	if rev := binary.LittleEndian.Uint32(b[8:12]); rev != revision {
		return h, fmt.Errorf("revision GPT 0x%08X, se esperaba 0x%08X", rev, revision)
	}
	size := binary.LittleEndian.Uint32(b[12:16])
	if size < HeaderSize || int(size) > len(b) {
		return h, fmt.Errorf("tamano de cabecera declarado %d, invalido", size)
	}

	want := binary.LittleEndian.Uint32(b[16:20])
	check := append([]byte(nil), b[:size]...)
	binary.LittleEndian.PutUint32(check[16:20], 0)
	if got := crc32.ChecksumIEEE(check); got != want {
		return h, fmt.Errorf("CRC de cabecera 0x%08X, calculado 0x%08X", want, got)
	}

	h.MyLBA = binary.LittleEndian.Uint64(b[24:32])
	h.AlternateLBA = binary.LittleEndian.Uint64(b[32:40])
	h.FirstUsable = binary.LittleEndian.Uint64(b[40:48])
	h.LastUsable = binary.LittleEndian.Uint64(b[48:56])
	copy(h.DiskGUID[:], b[56:72])
	h.PartitionEntryLBA = binary.LittleEndian.Uint64(b[72:80])
	h.NumEntries = binary.LittleEndian.Uint32(b[80:84])
	h.EntrySize = binary.LittleEndian.Uint32(b[84:88])
	h.EntriesCRC = binary.LittleEndian.Uint32(b[88:92])

	if h.NumEntries == 0 || h.EntrySize < EntrySize {
		return h, fmt.Errorf("tabla de entradas invalida: %d entradas de %d bytes", h.NumEntries, h.EntrySize)
	}
	return h, nil
}

// marshalEntries serializa las particiones en el formato de 128 bytes por
// entrada. Las posiciones libres quedan a cero, que es como GPT marca "sin usar".
func marshalEntries(parts []Entry) ([]byte, error) {
	if len(parts) > EntryCount {
		return nil, fmt.Errorf("%d particiones no caben en una tabla de %d", len(parts), EntryCount)
	}
	b := make([]byte, EntryCount*EntrySize)
	for i, p := range parts {
		off := i * EntrySize
		copy(b[off:off+16], p.Type[:])
		copy(b[off+16:off+32], p.Unique[:])
		binary.LittleEndian.PutUint64(b[off+32:off+40], p.FirstLBA)
		binary.LittleEndian.PutUint64(b[off+40:off+48], p.LastLBA)
		binary.LittleEndian.PutUint64(b[off+48:off+56], p.Attributes)

		units := utf16.Encode([]rune(p.Name))
		if len(units) > MaxNameUnits {
			return nil, fmt.Errorf("el nombre de la particion %d ocupa %d unidades UTF-16, maximo %d",
				i+1, len(units), MaxNameUnits)
		}
		for j, u := range units {
			binary.LittleEndian.PutUint16(b[off+56+j*2:], u)
		}
	}
	return b, nil
}

// parseEntries interpreta el array de entradas y descarta las libres.
func parseEntries(b []byte, count, size uint32) ([]Entry, error) {
	need := int(count) * int(size)
	if len(b) < need {
		return nil, fmt.Errorf("tabla de %d bytes, se esperaban %d", len(b), need)
	}

	out := make([]Entry, 0, count)
	for i := uint32(0); i < count; i++ {
		off := int(i) * int(size)
		raw := b[off : off+EntrySize]

		var e Entry
		copy(e.Type[:], raw[0:16])
		if e.Type.IsZero() {
			continue
		}
		copy(e.Unique[:], raw[16:32])
		e.FirstLBA = binary.LittleEndian.Uint64(raw[32:40])
		e.LastLBA = binary.LittleEndian.Uint64(raw[40:48])
		e.Attributes = binary.LittleEndian.Uint64(raw[48:56])

		units := make([]uint16, 0, MaxNameUnits)
		for j := 0; j < MaxNameUnits; j++ {
			u := binary.LittleEndian.Uint16(raw[56+j*2:])
			if u == 0 {
				break
			}
			units = append(units, u)
		}
		e.Name = string(utf16.Decode(units))
		out = append(out, e)
	}
	return out, nil
}

// Commit escribe la tabla en el dispositivo.
//
// El orden es de atras hacia delante, para que el disco siga pareciendo vacio
// hasta el ultimo momento: primero las entradas y la cabecera de respaldo,
// despues las entradas primarias, luego la cabecera primaria y por ultimo el MBR
// protector. Si el proceso muere a medias, lo mas probable es que el disco
// conserve su aspecto anterior.
func Commit(dev blockdev.Writable, t *Table, l Layout) error {
	if err := t.Validate(l); err != nil {
		return err
	}

	entries, err := marshalEntries(t.Partitions)
	if err != nil {
		return err
	}
	entriesCRC := crc32.ChecksumIEEE(entries)
	entrySectors := uint64(len(entries)) / uint64(l.SectorSize)

	backupEntriesLBA := l.TotalSectors - entrySectors - 1
	backupHeaderLBA := l.TotalSectors - 1

	primary := header{
		MyLBA:             1,
		AlternateLBA:      backupHeaderLBA,
		FirstUsable:       l.FirstUsable,
		LastUsable:        l.LastUsable,
		DiskGUID:          t.DiskGUID,
		PartitionEntryLBA: 2,
		NumEntries:        EntryCount,
		EntrySize:         EntrySize,
		EntriesCRC:        entriesCRC,
	}
	backup := primary
	backup.MyLBA = backupHeaderLBA
	backup.AlternateLBA = 1
	backup.PartitionEntryLBA = backupEntriesLBA

	steps := []struct {
		what string
		lba  uint64
		data []byte
	}{
		{"entradas de respaldo", backupEntriesLBA, entries},
		{"cabecera de respaldo", backupHeaderLBA, marshalHeader(backup)},
		{"entradas primarias", 2, entries},
		{"cabecera primaria", 1, marshalHeader(primary)},
	}
	for _, s := range steps {
		if err := writeSector(dev, s.lba, s.data, l.SectorSize); err != nil {
			return fmt.Errorf("escribir %s: %w", s.what, err)
		}
	}
	if err := dev.Sync(); err != nil {
		return fmt.Errorf("sincronizar tras la tabla: %w", err)
	}

	// El MBR protector va el ultimo: es lo primero que lee Windows y lo que hace
	// que el disco se presente como GPT.
	if err := writeSector(dev, 0, protectiveMBR(l.TotalSectors), l.SectorSize); err != nil {
		return fmt.Errorf("escribir MBR protector: %w", err)
	}
	if err := dev.Sync(); err != nil {
		return fmt.Errorf("sincronizar tras el MBR: %w", err)
	}
	return nil
}

// Read lee y valida la tabla del dispositivo. Sirve para verificar lo que acaba
// de escribirse sin depender de herramientas externas.
func Read(dev blockdev.Device) (*Table, Layout, error) {
	var empty Layout

	sectorSize := dev.SectorSize()
	layout, err := NewLayout(dev.Size(), sectorSize)
	if err != nil {
		return nil, empty, err
	}

	sector := make([]byte, sectorSize)
	if _, err := dev.ReadAt(sector, int64(sectorSize)); err != nil {
		return nil, empty, fmt.Errorf("leer la cabecera primaria: %w", err)
	}
	h, err := parseHeader(sector)
	if err != nil {
		return nil, empty, err
	}

	declared := int(h.NumEntries) * int(h.EntrySize)
	padded := (declared + sectorSize - 1) / sectorSize * sectorSize
	raw := make([]byte, padded)
	if _, err := dev.ReadAt(raw, int64(h.PartitionEntryLBA)*int64(sectorSize)); err != nil {
		return nil, empty, fmt.Errorf("leer la tabla de entradas: %w", err)
	}
	if got := crc32.ChecksumIEEE(raw[:declared]); got != h.EntriesCRC {
		return nil, empty, fmt.Errorf("CRC de entradas 0x%08X, calculado 0x%08X", h.EntriesCRC, got)
	}

	parts, err := parseEntries(raw[:declared], h.NumEntries, h.EntrySize)
	if err != nil {
		return nil, empty, err
	}

	layout.FirstUsable = h.FirstUsable
	layout.LastUsable = h.LastUsable
	layout.EntrySectors = uint64(padded) / uint64(sectorSize)

	return &Table{DiskGUID: h.DiskGUID, Partitions: parts}, layout, nil
}

// writeSector escribe datos alineados a sector, rellenando con ceros lo que
// falte.
func writeSector(dev blockdev.Writable, lba uint64, data []byte, sectorSize int) error {
	if rem := len(data) % sectorSize; rem != 0 {
		padded := make([]byte, len(data)+sectorSize-rem)
		copy(padded, data)
		data = padded
	}
	off := int64(lba) * int64(sectorSize)
	n, err := dev.WriteAt(data, off)
	if err != nil {
		return fmt.Errorf("LBA %d: %w", lba, err)
	}
	if n != len(data) {
		return fmt.Errorf("LBA %d: escritos %d de %d bytes", lba, n, len(data))
	}
	return nil
}
