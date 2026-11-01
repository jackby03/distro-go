//go:build windows

package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/gpt"
)

// memDevice es un dispositivo en memoria que cumple blockdev.Writable.
//
// Existe para poder ejercitar el bucle de copia de flash sin un USB conectado: es
// el corazon del comando y hasta ahora solo se habia probado a mano.
type memDevice struct {
	data   []byte
	sector int
	falla  error
}

var _ blockdev.Writable = (*memDevice)(nil)

func newMemDevice(size int64) *memDevice {
	return &memDevice{data: make([]byte, size), sector: 512}
}

func (m *memDevice) ReadAt(p []byte, off int64) (int, error) {
	if m.falla != nil {
		return 0, m.falla
	}
	if off < 0 || off+int64(len(p)) > int64(len(m.data)) {
		return 0, blockdev.ErrOutOfRange
	}
	return copy(p, m.data[off:]), nil
}

func (m *memDevice) WriteAt(p []byte, off int64) (int, error) {
	if m.falla != nil {
		return 0, m.falla
	}
	if off < 0 || off+int64(len(p)) > int64(len(m.data)) {
		return 0, blockdev.ErrOutOfRange
	}
	return copy(m.data[off:], p), nil
}

func (m *memDevice) Size() int64     { return int64(len(m.data)) }
func (m *memDevice) SectorSize() int { return m.sector }
func (m *memDevice) Sync() error     { return nil }
func (m *memDevice) Close() error    { return nil }

// imagenDePrueba escribe un fichero con contenido reconocible y tamano arbitrario.
func imagenDePrueba(t *testing.T, size int) (string, []byte) {
	t.Helper()

	contenido := make([]byte, size)
	for i := range contenido {
		contenido[i] = byte(i * 7)
	}

	path := filepath.Join(t.TempDir(), "imagen.img")
	if err := os.WriteFile(path, contenido, 0o600); err != nil {
		t.Fatalf("escribir la imagen: %v", err)
	}
	return path, contenido
}

func TestCopyToDiskCopiaLaImagenEntera(t *testing.T) {
	// 3000 bytes: no es multiplo de sector ni del bloque de copia, que es justo el
	// caso del ultimo trozo.
	path, contenido := imagenDePrueba(t, 3000)
	dev := newMemDevice(8192)

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	escritos, err := app.copyToDisk(path, dev, int64(len(contenido)))
	if err != nil {
		t.Fatalf("copyToDisk: %v", err)
	}
	if escritos != int64(len(contenido)) {
		t.Fatalf("escritos = %d, queria %d", escritos, len(contenido))
	}
	if !bytes.Equal(dev.data[:len(contenido)], contenido) {
		t.Fatal("lo copiado no coincide con la imagen")
	}
	// Y lo que sobra del dispositivo sigue a cero: no se escribe de mas.
	for i := len(contenido); i < len(dev.data); i++ {
		if dev.data[i] != 0 {
			t.Fatalf("se escribio de mas en el byte %d", i)
		}
	}
}

func TestCopyToDiskFallaSiNoCabe(t *testing.T) {
	path, _ := imagenDePrueba(t, 4096)
	dev := newMemDevice(1024)

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if _, err := app.copyToDisk(path, dev, 4096); err == nil {
		t.Fatal("una imagen que no cabe tiene que fallar")
	}
}

func TestCopyToDiskPropagaElErrorDelDispositivo(t *testing.T) {
	path, _ := imagenDePrueba(t, 1024)
	dev := newMemDevice(8192)
	dev.falla = fmt.Errorf("el disco se desconecto")

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	_, err := app.copyToDisk(path, dev, 1024)
	if err == nil || !strings.Contains(err.Error(), "se desconecto") {
		t.Fatalf("err = %v, queria el error del dispositivo", err)
	}
}

func TestVerifyDiskDetectaDiferencias(t *testing.T) {
	path, contenido := imagenDePrueba(t, 2048)
	dev := newMemDevice(4096)

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if _, err := app.copyToDisk(path, dev, int64(len(contenido))); err != nil {
		t.Fatalf("copyToDisk: %v", err)
	}

	// Igual: no debe quejarse.
	if err := app.verifyDisk(path, dev, int64(len(contenido))); err != nil {
		t.Fatalf("una copia correcta no deberia fallar: %v", err)
	}

	// Un byte cambiado en el disco: tiene que decir donde.
	dev.data[1500] ^= 0xFF
	err := app.verifyDisk(path, dev, int64(len(contenido)))
	if err == nil {
		t.Fatal("una diferencia tiene que detectarse")
	}
	if !strings.Contains(err.Error(), "1500") {
		t.Fatalf("el error deberia indicar el byte 1500: %v", err)
	}
}

func TestValidateImageRechazaLoQueNoEsUnaInstalacion(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	// Un fichero de ceros: sin GPT.
	vacio := filepath.Join(t.TempDir(), "vacio.img")
	if err := os.WriteFile(vacio, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	if err := app.validateImage(vacio); err == nil {
		t.Fatal("un fichero sin GPT no puede aceptarse")
	}

	// Una imagen que si tiene GPT y las dos particiones: se acepta.
	buena := filepath.Join(t.TempDir(), "buena.img")
	img, err := blockdev.CreateImage(buena, 16<<20)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	layout, err := gpt.NewLayout(16<<20, 512)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	entradas, err := gpt.Plan(layout, []gpt.SizeSpec{
		{Name: "ESP", Type: gpt.TypeESP, SizeBytes: 4 << 20},
		{Name: "root", Type: gpt.TypeLinuxFilesystem},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	tabla, err := gpt.NewTable(gpt.GUID{}, entradas)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	if err := gpt.Commit(img, tabla, layout); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := app.validateImage(buena); err != nil {
		t.Fatalf("una imagen buena no deberia rechazarse: %v", err)
	}
}

func TestFlashRechazaAntesDeAbrirNada(t *testing.T) {
	app, _, errBuf := newTestApp(fakeInventory())
	imagen := crearImagenValida(t)

	casos := []struct {
		nombre string
		args   []string
		quiere string
	}{
		{"sin imagen", []string{"flash", "--target-disk", "3"}, "falta --image"},
		{"sin disco", []string{"flash", "--image", imagen}, "falta --target-disk"},
		{"disco inexistente", []string{"flash", "--image", imagen, "--target-disk", "9"}, "no existe el disco 9"},
	}

	for _, c := range casos {
		errBuf.Reset()
		if code := app.Run(c.args); code == exitOK {
			t.Errorf("%s: no puede terminar bien", c.nombre)
		}
		if !strings.Contains(errBuf.String(), c.quiere) {
			t.Errorf("%s: esperaba %q en %q", c.nombre, c.quiere, errBuf.String())
		}
	}
}

func TestFlashRechazaElDiscoDelSistema(t *testing.T) {
	app, _, errBuf := newTestApp(fakeInventory())

	imagen := crearImagenValida(t)
	code := app.Run([]string{"flash", "--image", imagen, "--target-disk", "1",
		"--expect-serial", "SN-NVME-0001"})

	if code == exitOK {
		t.Fatal("el disco del sistema no puede ser objetivo")
	}
	if !strings.Contains(errBuf.String(), "aloja el Windows") {
		t.Fatalf("no explico el motivo: %q", errBuf.String())
	}
}

func TestFlashRechazaUnSerialQueNoCoincide(t *testing.T) {
	app, _, errBuf := newTestApp(fakeInventory())
	imagen := crearImagenValida(t)

	code := app.Run([]string{"flash", "--image", imagen, "--target-disk", "3",
		"--expect-serial", "SERIAL-QUE-NO-ES"})
	if code == exitOK {
		t.Fatal("un serial que no coincide tiene que rechazarse")
	}
	if !strings.Contains(errBuf.String(), "serial") {
		t.Fatalf("no menciono el serial: %q", errBuf.String())
	}
}

// crearImagenValida construye un .img con GPT, ESP y root, como el que genera
// partition. Sirve para que las pruebas de guardas lleguen mas alla de la
// validacion de la imagen.
func crearImagenValida(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "valida.img")
	img, err := blockdev.CreateImage(path, 16<<20)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}

	layout, err := gpt.NewLayout(16<<20, 512)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	entradas, err := gpt.Plan(layout, []gpt.SizeSpec{
		{Name: "ESP", Type: gpt.TypeESP, SizeBytes: 4 << 20},
		{Name: "root", Type: gpt.TypeLinuxFilesystem},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	tabla, err := gpt.NewTable(gpt.GUID{}, entradas)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	if err := gpt.Commit(img, tabla, layout); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func TestInstallRechazaAntesDeAbrirNada(t *testing.T) {
	app, _, errBuf := newTestApp(fakeInventory())

	casos := []struct {
		nombre string
		args   []string
		quiere string
	}{
		{"sin disco", []string{"install"}, "falta --target-disk"},
		{"apply sin iso", []string{"install", "--target-disk", "3", "--apply"}, "--apply necesita --iso"},
		{"disco inexistente", []string{"install", "--target-disk", "9",
			"--expect-serial", "SN-PRUEBAS-0001"}, "no existe el disco 9"},
		{"serial que no coincide", []string{"install", "--target-disk", "3",
			"--expect-serial", "OTRO"}, "serial"},
	}

	for _, c := range casos {
		errBuf.Reset()
		if code := app.Run(c.args); code == exitOK {
			t.Errorf("%s: no puede terminar bien", c.nombre)
		}
		if !strings.Contains(errBuf.String(), c.quiere) {
			t.Errorf("%s: esperaba %q en %q", c.nombre, c.quiere, errBuf.String())
		}
	}
}

func TestInstallRechazaElDiscoDelSistema(t *testing.T) {
	app, _, errBuf := newTestApp(fakeInventory())

	code := app.Run([]string{"install", "--target-disk", "1", "--expect-serial", "SN-NVME-0001"})
	if code == exitOK {
		t.Fatal("el disco del sistema no puede ser objetivo")
	}
	if !strings.Contains(errBuf.String(), "aloja el Windows") {
		t.Fatalf("no explico el motivo: %q", errBuf.String())
	}
}
