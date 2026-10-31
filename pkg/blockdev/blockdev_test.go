package blockdev

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// testSize es pequeno a proposito: en NTFS una imagen truncada de 8 GiB ocupa
// 8 GiB reales. Los tests no necesitan ese tamano, solo el contrato.
const testSize = 4 << 20 // 4 MiB

func newTestImage(t *testing.T) (*Image, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.img")
	img, err := CreateImage(path, testSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	t.Cleanup(func() { _ = img.Close() })
	return img, path
}

func TestCreateImageReportsSizeAndSector(t *testing.T) {
	img, path := newTestImage(t)

	if got := img.Size(); got != testSize {
		t.Fatalf("Size = %d, se esperaba %d", got, testSize)
	}
	if got := img.SectorSize(); got != DefaultSectorSize {
		t.Fatalf("SectorSize = %d, se esperaba %d", got, DefaultSectorSize)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() != testSize {
		t.Fatalf("tamano en disco = %d, se esperaba %d", st.Size(), testSize)
	}
}

func TestCreateImageRejectsInvalidSize(t *testing.T) {
	dir := t.TempDir()
	for _, size := range []int64{0, -1} {
		if _, err := CreateImage(filepath.Join(dir, "x.img"), size); !errors.Is(err, ErrInvalidSize) {
			t.Fatalf("tamano %d: error = %v, se esperaba ErrInvalidSize", size, err)
		}
	}
}

func TestImageIsZeroFilledAndRoundTrips(t *testing.T) {
	img, _ := newTestImage(t)

	zeros := make([]byte, 4096)
	if _, err := img.ReadAt(zeros, 0); err != nil {
		t.Fatalf("ReadAt inicial: %v", err)
	}
	if !bytes.Equal(zeros, make([]byte, 4096)) {
		t.Fatal("una imagen recien creada deberia leerse como ceros")
	}

	want := []byte("distrogo-blockdev")
	const off = int64(4096)
	if n, err := img.WriteAt(want, off); err != nil || n != len(want) {
		t.Fatalf("WriteAt = (%d, %v), se esperaba (%d, nil)", n, err, len(want))
	}

	got := make([]byte, len(want))
	if _, err := img.ReadAt(got, off); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contenido = %q, se esperaba %q", got, want)
	}
}

func TestImageWriteAtBeyondEnd(t *testing.T) {
	img, _ := newTestImage(t)

	if _, err := img.WriteAt([]byte("x"), img.Size()-1); err != nil {
		t.Fatalf("escribir el ultimo byte no deberia fallar: %v", err)
	}

	if _, err := img.WriteAt([]byte("xx"), img.Size()-1); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("error = %v, se esperaba ErrOutOfRange", err)
	}
	if _, err := img.WriteAt([]byte("x"), img.Size()); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("error = %v, se esperaba ErrOutOfRange", err)
	}

	// El dispositivo no debe haber crecido.
	st, err := img.f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() != testSize {
		t.Fatalf("el dispositivo crecio a %d bytes", st.Size())
	}
}

func TestImageReadAtBeyondEndReturnsEOF(t *testing.T) {
	img, _ := newTestImage(t)

	buf := make([]byte, 4)
	_, err := img.ReadAt(buf, img.Size()-2)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, se esperaba io.EOF", err)
	}
}

func TestReadOnlyImageRejectsWrites(t *testing.T) {
	img, path := newTestImage(t)
	want := []byte("persistente")
	if _, err := img.WriteAt(want, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := img.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := OpenImage(path, false)
	if err != nil {
		t.Fatalf("OpenImage: %v", err)
	}
	defer ro.Close()

	if _, err := ro.WriteAt([]byte("x"), 0); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("error = %v, se esperaba ErrNotWritable", err)
	}

	got := make([]byte, len(want))
	if _, err := ro.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contenido = %q, se esperaba %q", got, want)
	}
}

func TestWritableImagePersistsAcrossReopen(t *testing.T) {
	img, path := newTestImage(t)
	want := []byte("sobrevive al cierre")
	const off = int64(512)
	if _, err := img.WriteAt(want, off); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := img.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rw, err := OpenImage(path, true)
	if err != nil {
		t.Fatalf("OpenImage: %v", err)
	}
	defer rw.Close()

	got := make([]byte, len(want))
	if _, err := rw.ReadAt(got, off); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contenido = %q, se esperaba %q", got, want)
	}
}

func TestCreateImageTruncatesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.img")

	big, err := CreateImage(path, 2*testSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	if _, err := big.WriteAt([]byte("relleno"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := big.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	small, err := CreateImage(path, testSize)
	if err != nil {
		t.Fatalf("CreateImage sobre fichero existente: %v", err)
	}
	defer small.Close()

	if got := small.Size(); got != testSize {
		t.Fatalf("Size = %d, se esperaba %d", got, testSize)
	}
	buf := make([]byte, 8)
	if _, err := small.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(buf, make([]byte, 8)) {
		t.Fatal("tras truncar, el contenido anterior deberia haber desaparecido")
	}
}

func TestSetSectorSize(t *testing.T) {
	img, _ := newTestImage(t)

	if err := img.SetSectorSize(4096); err != nil {
		t.Fatalf("SetSectorSize: %v", err)
	}
	if got := img.SectorSize(); got != 4096 {
		t.Fatalf("SectorSize = %d, se esperaba 4096", got)
	}
	if err := img.SetSectorSize(0); err == nil {
		t.Fatal("SetSectorSize(0) deberia fallar")
	}
}

func TestBusTypeString(t *testing.T) {
	cases := []struct {
		bus  BusType
		want string
	}{
		{BusUnknown, "desconocido"},
		{BusSATA, "sata"},
		{BusNVMe, "nvme"},
		{BusUSB, "usb"},
		{BusRAID, "raid"},
		{BusVirtual, "virtual"},
		{BusSD, "sd"},
		{BusType(200), "desconocido"},
	}
	for _, c := range cases {
		if got := c.bus.String(); got != c.want {
			t.Errorf("BusType(%d).String() = %q, se esperaba %q", c.bus, got, c.want)
		}
	}
}

func TestVolumeMounted(t *testing.T) {
	if (Volume{}).Mounted() {
		t.Fatal("un volumen sin punto de montaje no deberia estar montado")
	}
	if !(Volume{MountPoint: `C:\`}).Mounted() {
		t.Fatal("un volumen con punto de montaje deberia estar montado")
	}
}

func TestNegativeOffsetsAreRejected(t *testing.T) {
	img, _ := newTestImage(t)

	buf := make([]byte, 4)
	if _, err := img.ReadAt(buf, -1); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("ReadAt(-1): error = %v, se esperaba os.ErrInvalid", err)
	}
	if _, err := img.WriteAt(buf, -1); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("WriteAt(-1): error = %v, se esperaba os.ErrInvalid", err)
	}
}

func TestOpenImageMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-existe.img")
	if _, err := OpenImage(path, false); err == nil {
		t.Fatal("OpenImage sobre un fichero inexistente deberia fallar")
	}
}
