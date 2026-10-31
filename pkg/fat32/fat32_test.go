package fat32

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"

	"distrogo/pkg/blockdev"
)

// 40 MiB obliga a clusters de 512 bytes (la tabla de Windows daria 512 por
// debajo de 260 MiB) y sigue teniendo mas de 65525 clusters.
const smallVolume = 40 << 20

// 300 MiB entra en el tramo de 4 KiB de la tabla de Windows.
const mediumVolume = 300 << 20

func newImage(t *testing.T, size int64) *blockdev.Image {
	t.Helper()
	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "fs.img"), size)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	t.Cleanup(func() { _ = img.Close() })
	return img
}

func readBytes(t *testing.T, dev blockdev.Device, off int64, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := dev.ReadAt(buf, off); err != nil {
		t.Fatalf("ReadAt(%d, %d): %v", off, n, err)
	}
	return buf
}

func TestFormatSmallVolume(t *testing.T) {
	img := newImage(t, smallVolume)

	fs, err := Format(img, Options{VolumeID: 0x1234ABCD})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	if info.ClusterSize != 512 {
		t.Errorf("ClusterSize = %d, se esperaban 512", info.ClusterSize)
	}
	if info.SectorSize != 512 {
		t.Errorf("SectorSize = %d", info.SectorSize)
	}
	if info.Clusters < minClusters {
		t.Errorf("Clusters = %d, por debajo del minimo %d", info.Clusters, minClusters)
	}
	if want := uint64(smallVolume) / 512; info.TotalSectors != want {
		t.Errorf("TotalSectors = %d, se esperaban %d", info.TotalSectors, want)
	}
	if info.DataStart != reservedSectors+numFATs*info.FatSectors {
		t.Errorf("DataStart = %d", info.DataStart)
	}
	if info.Label != "DISTROGO" {
		t.Errorf("Label = %q", info.Label)
	}
	if info.VolumeID != 0x1234ABCD {
		t.Errorf("VolumeID = 0x%08X", info.VolumeID)
	}
}

func TestFormatMediumVolume(t *testing.T) {
	img := newImage(t, mediumVolume)

	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	if info.ClusterSize != 4096 {
		t.Errorf("ClusterSize = %d, se esperaban 4096", info.ClusterSize)
	}
	if info.Clusters < minClusters {
		t.Errorf("Clusters = %d, por debajo del minimo", info.Clusters)
	}
}

func TestFormatTooSmall(t *testing.T) {
	img := newImage(t, 8<<20)

	if _, err := Format(img, Options{}); !errors.Is(err, ErrTooSmall) {
		t.Fatalf("error = %v, se esperaba ErrTooSmall", err)
	}
}

func TestBootSectorFields(t *testing.T) {
	img := newImage(t, mediumVolume)
	fs, err := Format(img, Options{Label: "EFI", VolumeID: 0xDEADBEEF})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	b := readBytes(t, img, 0, 512)

	if got := string(b[3:11]); got != "DISTROGO" {
		t.Errorf("OEM = %q", got)
	}
	if got := binary.LittleEndian.Uint16(b[0x0B:]); got != 512 {
		t.Errorf("bytes por sector = %d", got)
	}
	if got := b[0x0D]; got != 8 {
		t.Errorf("sectores por cluster = %d, se esperaban 8", got)
	}
	if got := binary.LittleEndian.Uint16(b[0x0E:]); got != reservedSectors {
		t.Errorf("sectores reservados = %d", got)
	}
	if got := b[0x10]; got != numFATs {
		t.Errorf("numero de FATs = %d", got)
	}
	if got := binary.LittleEndian.Uint16(b[0x11:]); got != 0 {
		t.Errorf("entradas de raiz = %d, deberia ser 0 en FAT32", got)
	}
	if got := binary.LittleEndian.Uint32(b[0x20:]); uint64(got) != info.TotalSectors {
		t.Errorf("sectores totales = %d, se esperaban %d", got, info.TotalSectors)
	}
	if got := binary.LittleEndian.Uint32(b[0x24:]); uint64(got) != info.FatSectors {
		t.Errorf("sectores de la FAT = %d, se esperaban %d", got, info.FatSectors)
	}
	if got := binary.LittleEndian.Uint32(b[0x2C:]); got != rootCluster {
		t.Errorf("cluster raiz = %d", got)
	}
	if got := binary.LittleEndian.Uint16(b[0x30:]); got != 1 {
		t.Errorf("sector de FSInfo = %d", got)
	}
	if got := binary.LittleEndian.Uint16(b[0x32:]); got != 6 {
		t.Errorf("sector de respaldo = %d", got)
	}
	if b[0x42] != 0x29 {
		t.Errorf("falta la firma extendida: 0x%02X", b[0x42])
	}
	if got := binary.LittleEndian.Uint32(b[0x43:]); got != 0xDEADBEEF {
		t.Errorf("identificador de volumen = 0x%08X", got)
	}
	if got := string(b[0x47:0x52]); got != "EFI        " {
		t.Errorf("etiqueta = %q", got)
	}
	if got := string(b[0x52:0x5A]); got != "FAT32   " {
		t.Errorf("tipo = %q", got)
	}
	if b[510] != 0x55 || b[511] != 0xAA {
		t.Errorf("firma de arranque = %02x %02x", b[510], b[511])
	}

	// El respaldo del sector de arranque tiene que ser identico.
	backup := readBytes(t, img, 6*512, 512)
	if !bytes.Equal(b, backup) {
		t.Error("el respaldo del sector de arranque no coincide")
	}
}

func TestFSInfo(t *testing.T) {
	img := newImage(t, mediumVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	for _, sector := range []int64{1, 7} {
		b := readBytes(t, img, sector*512, 512)

		if got := binary.LittleEndian.Uint32(b[0:]); got != 0x41615252 {
			t.Errorf("sector %d: firma inicial 0x%08X", sector, got)
		}
		if got := binary.LittleEndian.Uint32(b[484:]); got != 0x61417272 {
			t.Errorf("sector %d: firma de estructura 0x%08X", sector, got)
		}
		if got := binary.LittleEndian.Uint32(b[488:]); uint64(got) != info.Clusters-1 {
			t.Errorf("sector %d: clusters libres = %d, se esperaban %d", sector, got, info.Clusters-1)
		}
		if got := binary.LittleEndian.Uint32(b[492:]); got != rootCluster+1 {
			t.Errorf("sector %d: siguiente libre = %d", sector, got)
		}
		if got := binary.LittleEndian.Uint32(b[508:]); got != 0xAA550000 {
			t.Errorf("sector %d: firma final 0x%08X", sector, got)
		}
	}
}

func TestFATInitialEntries(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	fatOff := int64(reservedSectors * 512)
	head := readBytes(t, img, fatOff, 32)

	if got := binary.LittleEndian.Uint32(head[0:]) & 0x0FFFFFFF; got != 0x0FFFFFF8 {
		t.Errorf("FAT[0] = 0x%08X, se esperaba 0x0FFFFFF8", got)
	}
	if got := binary.LittleEndian.Uint32(head[4:]) & 0x0FFFFFFF; got != eocMarker {
		t.Errorf("FAT[1] = 0x%08X, se esperaba fin de cadena", got)
	}
	if got := binary.LittleEndian.Uint32(head[8:]) & 0x0FFFFFFF; got != eocMarker {
		t.Errorf("FAT[2] = 0x%08X, el raiz deberia estar cerrado", got)
	}
	if got := binary.LittleEndian.Uint32(head[12:]); got != 0 {
		t.Errorf("FAT[3] = 0x%08X, deberia estar libre", got)
	}

	// El resto de la tabla, hasta el final, tiene que estar a cero.
	rest := readBytes(t, img, fatOff+32, int(info.FatSectors*512)-32)
	for i, b := range rest {
		if b != 0 {
			t.Fatalf("la FAT no esta limpia en el byte %d", i)
		}
	}
}

func TestBothFATCopiesIdentical(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	size := int(info.FatSectors * 512)
	first := readBytes(t, img, int64(reservedSectors*512), size)
	second := readBytes(t, img, int64((reservedSectors+info.FatSectors)*512), size)

	if !bytes.Equal(first, second) {
		t.Fatal("las dos copias de la FAT no coinciden")
	}
}

func TestRootDirectoryHasOnlyTheLabelEntry(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{Label: "EFI"})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	info := fs.Info()

	root := readBytes(t, img, fs.clusterOffset(rootCluster), info.ClusterSize)

	if got := string(root[0:11]); got != "EFI        " {
		t.Errorf("nombre de la etiqueta = %q", got)
	}
	if got := root[11]; got != 0x08 {
		t.Errorf("atributo = 0x%02X, se esperaba 0x08 (etiqueta de volumen)", got)
	}
	// El resto de la entrada va a cero: sin tiempo, sin cluster, sin tamano.
	for i := 12; i < 32; i++ {
		if root[i] != 0 {
			t.Fatalf("byte %d de la entrada de etiqueta = 0x%02X", i, root[i])
		}
	}
	// Y detras de la entrada, el cluster entero esta a cero.
	for i := 32; i < len(root); i++ {
		if root[i] != 0 {
			t.Fatalf("el directorio raiz no esta limpio en el byte %d", i)
		}
	}
}

func TestFormatTwiceGivesSameBytes(t *testing.T) {
	img := newImage(t, smallVolume)

	if _, err := Format(img, Options{VolumeID: 0x01020304}); err != nil {
		t.Fatalf("primer Format: %v", err)
	}
	first := readBytes(t, img, 0, 512)

	if _, err := Format(img, Options{VolumeID: 0x01020304}); err != nil {
		t.Fatalf("segundo Format: %v", err)
	}
	second := readBytes(t, img, 0, 512)

	if !bytes.Equal(first, second) {
		t.Fatal("formatear dos veces con el mismo identificador deberia dar los mismos bytes")
	}
}

func TestFormatWipesPreviousContent(t *testing.T) {
	img := newImage(t, smallVolume)

	// Basura donde luego iran las estructuras.
	junk := bytes.Repeat([]byte{0xAA}, 4096)
	if _, err := img.WriteAt(junk, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if _, err := img.WriteAt(junk, int64(reservedSectors*512)); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}

	if _, err := Format(img, Options{}); err != nil {
		t.Fatalf("Format: %v", err)
	}

	if got := readBytes(t, img, 0, 512); !bytes.Equal(got[0:3], []byte{0xEB, 0x58, 0x90}) {
		t.Fatal("el sector de arranque no se reescribio")
	}
	fatHead := readBytes(t, img, int64(reservedSectors*512), 16)
	if bytes.Contains(fatHead, []byte{0xAA}) {
		t.Fatal("la FAT conserva restos del contenido anterior")
	}
}

func TestLabelValidation(t *testing.T) {
	img := newImage(t, smallVolume)

	if _, err := Format(img, Options{Label: "ETIQUETA-LARGA"}); !errors.Is(err, ErrBadOption) {
		t.Errorf("una etiqueta de 14 caracteres deberia fallar, devolvio %v", err)
	}
	if _, err := Format(img, Options{Label: `MALO:SI`}); !errors.Is(err, ErrBadOption) {
		t.Errorf("una etiqueta con dos puntos deberia fallar, devolvio %v", err)
	}

	fs, err := Format(img, Options{Label: "efi"})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if fs.Label() != "EFI" {
		t.Errorf("la etiqueta deberia normalizarse a mayusculas, es %q", fs.Label())
	}
}

func TestExplicitClusterSize(t *testing.T) {
	img := newImage(t, mediumVolume)

	fs, err := Format(img, Options{ClusterSize: 2048})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if got := fs.Info().ClusterSize; got != 2048 {
		t.Errorf("ClusterSize = %d", got)
	}

	if _, err := Format(img, Options{ClusterSize: 1000}); !errors.Is(err, ErrBadOption) {
		t.Error("un tamano de cluster que no es potencia de dos deberia fallar")
	}
	if _, err := Format(img, Options{ClusterSize: 65536}); !errors.Is(err, ErrBadOption) {
		t.Error("un cluster de 64 KiB deberia fallar")
	}
}

func TestFormatRejectsBadSectorSize(t *testing.T) {
	img := newImage(t, smallVolume)
	if err := img.SetSectorSize(768); err != nil {
		t.Fatalf("SetSectorSize: %v", err)
	}

	if _, err := Format(img, Options{}); !errors.Is(err, ErrBadOption) {
		t.Fatalf("error = %v, se esperaba ErrBadOption", err)
	}
}
