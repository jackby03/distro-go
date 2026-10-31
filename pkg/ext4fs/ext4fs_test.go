package ext4fs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
)

const (
	// El volumen va dentro de una imagen mayor, con offset, que es como se usara
	// en una particion real.
	volumeOffset = 1 << 20
	volumeSize   = 64 << 20
	imageSize    = volumeOffset + volumeSize + (1 << 20)
)

func newImage(t *testing.T, size int64) *blockdev.Image {
	t.Helper()
	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "ext4.img"), size)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	t.Cleanup(func() { _ = img.Close() })
	return img
}

func TestFormatWritesAValidSuperblock(t *testing.T) {
	img := newImage(t, imageSize)

	const wantUUID = "12345678-1234-4234-8234-123456789abc"
	fs, err := Format(img, volumeOffset, volumeSize, Options{
		Label:      "distrogo-root",
		UUID:       wantUUID,
		InodeCount: 20000,
	})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	info, err := fs.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.UUID != wantUUID {
		t.Errorf("UUID = %q, se esperaba %q", info.UUID, wantUUID)
	}
	if info.Label != "distrogo-root" {
		t.Errorf("etiqueta = %q", info.Label)
	}
	if info.BlockSize != DefaultBlockSize {
		t.Errorf("bloque = %d, se esperaba %d", info.BlockSize, DefaultBlockSize)
	}
	if info.InodeCount < 20000 {
		t.Errorf("inodos = %d, se esperaban al menos 20000", info.InodeCount)
	}
	if info.FreeBytes <= 0 || info.FreeBytes > volumeSize {
		t.Errorf("libre = %d", info.FreeBytes)
	}

	// El superbloque tiene que estar donde dice la especificacion.
	buf := make([]byte, 1024)
	if _, err := img.ReadAt(buf, volumeOffset+1024); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if got := binary.LittleEndian.Uint16(buf[0x38:]); got != 0xEF53 {
		t.Errorf("magic = 0x%04X, se esperaba 0xEF53", got)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFormatDoesNotTouchOutsideTheSection(t *testing.T) {
	img := newImage(t, imageSize)

	marker := bytes.Repeat([]byte{0xAA}, 4096)
	if _, err := img.WriteAt(marker, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if _, err := img.WriteAt(marker, volumeOffset+volumeSize); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}

	fs, err := Format(img, volumeOffset, volumeSize, Options{InodeCount: 20000})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before := make([]byte, 4096)
	if _, err := img.ReadAt(before, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(before, marker) {
		t.Error("se escribio antes de la seccion")
	}
	if _, err := img.ReadAt(before, volumeOffset+volumeSize); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(before, marker) {
		t.Error("se escribio despues de la seccion")
	}
}

func TestMkdirAndWriteFile(t *testing.T) {
	img := newImage(t, imageSize)

	fs, err := Format(img, volumeOffset, volumeSize, Options{Label: "pruebas"})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	content := []byte("contenido dentro de ext4\n")
	if err := fs.WriteFile("etc/motd", bytes.NewReader(content)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Mkdir("home/usuario"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Volver a montar y leer lo escrito.
	reopened, err := Open(img, volumeOffset, volumeSize)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.efs.ReadFile("etc/motd")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("leido %q, se esperaba %q", got, content)
	}
	if _, err := reopened.efs.Stat("home/usuario"); err != nil {
		t.Fatalf("el directorio no sobrevivio: %v", err)
	}

	info, err := reopened.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Label != "pruebas" {
		t.Errorf("etiqueta releida = %q", info.Label)
	}
	if info.FreeBytes >= volumeSize {
		t.Error("despues de escribir, el volumen deberia tener menos espacio libre")
	}
}

func TestFormatRejectsBadRanges(t *testing.T) {
	img := newImage(t, imageSize)

	cases := []struct {
		name   string
		offset int64
		size   int64
	}{
		{"demasiado pequeno", 0, 1024},
		{"offset negativo", -1, volumeSize},
		{"pasa del dispositivo", 0, imageSize + 1},
	}
	for _, c := range cases {
		if _, err := Format(img, c.offset, c.size, Options{}); !errors.Is(err, ErrBadSize) {
			t.Errorf("%s: error = %v, se esperaba ErrBadSize", c.name, err)
		}
	}
}

func TestFormatRejectsBadUUID(t *testing.T) {
	img := newImage(t, imageSize)

	_, err := Format(img, volumeOffset, volumeSize, Options{UUID: "no-es-un-uuid"})
	if err == nil {
		t.Fatal("un UUID invalido deberia fallar")
	}
	if !strings.Contains(err.Error(), "UUID") {
		t.Errorf("el error deberia mencionar el UUID: %v", err)
	}
}

func TestOpenRejectsWhatIsNotExt4(t *testing.T) {
	img := newImage(t, imageSize)

	if _, err := Open(img, volumeOffset, volumeSize); !errors.Is(err, ErrNotExt4) && err == nil {
		t.Fatal("una seccion vacia no deberia montarse")
	}
}

func TestDefaultInodeCount(t *testing.T) {
	if got := defaultInodeCount(8 << 30); got != uint32((8<<30)/inodeRatio) {
		t.Errorf("para 8 GiB = %d", got)
	}
	if got := defaultInodeCount(64 << 20); got != uint32((64<<20)/inodeRatio) {
		t.Errorf("para 64 MiB = %d", got)
	}
	if got := defaultInodeCount(1 << 20); got != minInodes {
		t.Errorf("para 1 MiB = %d, se esperaba el minimo %d", got, minInodes)
	}
}

func TestSectionInfoIsAFile(t *testing.T) {
	img := newImage(t, imageSize)
	st := &storage{dev: img, offset: volumeOffset, size: volumeSize}

	fi, err := st.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != volumeSize || fi.IsDir() {
		t.Errorf("Stat = %d bytes, dir %v", fi.Size(), fi.IsDir())
	}
	if fi.Mode()&0o444 == 0 {
		t.Errorf("mode = %o", fi.Mode())
	}

	// Seek al final tiene que dar el tamano de la seccion, no el del dispositivo.
	pos, err := st.Seek(0, 2)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if pos != volumeSize {
		t.Errorf("Seek(0,2) = %d, se esperaba %d", pos, volumeSize)
	}

	if _, err := st.Seek(-1, 0); err == nil {
		t.Error("un seek negativo deberia fallar")
	}
}

// TestOverwriteFileKeepsNeighboursIntact es la prueba de regresion del fallo que
// costo la corrupcion de M6: reescribir un fichero de /etc no puede tocar a sus
// vecinos.
func TestOverwriteFileKeepsNeighboursIntact(t *testing.T) {
	img := newImage(t, imageSize)
	fs, err := Format(img, volumeOffset, volumeSize, Options{InodeCount: 4000})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	vecinos := map[string]string{
		"etc/sudoers":  "%sudo ALL=(ALL:ALL) ALL\n",
		"etc/hostname": "mint\n",
		"etc/shells":   "/bin/sh\n/bin/bash\n",
		"etc/timezone": "Europe/Madrid\n",
	}
	for path, body := range vecinos {
		if err := fs.WriteFile(path, strings.NewReader(body)); err != nil {
			t.Fatalf("WriteFile %q: %v", path, err)
		}
	}
	if err := fs.WriteFile("etc/group", strings.NewReader("root:x:0:\nsudo:x:27:\n")); err != nil {
		t.Fatalf("WriteFile group: %v", err)
	}

	// Reescritura que ademas crece respecto al original.
	nuevo := "root:x:0:\nsudo:x:27:ana\nadm:x:4:syslog\n"
	if err := fs.OverwriteFile("etc/group", []byte(nuevo)); err != nil {
		t.Fatalf("OverwriteFile: %v", err)
	}

	if got := leerFichero(t, fs, "etc/group"); got != nuevo {
		t.Fatalf("group = %q, queria %q", got, nuevo)
	}
	for path, quiero := range vecinos {
		if got := leerFichero(t, fs, path); got != quiero {
			t.Fatalf("%q cambio al reescribir otro fichero: %q", path, got)
		}
	}
}

// TestOverwriteFilePadsShorterContent comprueba el relleno cuando el contenido
// nuevo es mas corto: go-diskfs no trunca, asi que sobran bytes al final.
func TestOverwriteFilePadsShorterContent(t *testing.T) {
	img := newImage(t, imageSize)
	fs, err := Format(img, volumeOffset, volumeSize, Options{InodeCount: 4000})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	largo := "# fstab de la distro, con varias lineas\n# y mas texto de relleno\nUUID=viejo / ext4 defaults 0 1\n"
	if err := fs.WriteFile("etc/fstab", strings.NewReader(largo)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	corto := "UUID=nuevo / ext4 errors=remount-ro 0 1\n"
	if err := fs.OverwriteFile("etc/fstab", []byte(corto)); err != nil {
		t.Fatalf("OverwriteFile: %v", err)
	}

	got := leerFichero(t, fs, "etc/fstab")
	if !strings.HasPrefix(got, corto) {
		t.Fatalf("no empieza por el contenido nuevo: %q", got)
	}
	// Lo que sobra solo pueden ser saltos de linea: cualquier otra cosa seria
	// basura del contenido anterior.
	if resto := strings.TrimPrefix(got, corto); strings.Trim(resto, "\n") != "" {
		t.Fatalf("quedo basura del contenido anterior: %q", resto)
	}
}

// TestAppendFileKeepsContent comprueba que anadir no toca lo que ya habia.
func TestAppendFileKeepsContent(t *testing.T) {
	img := newImage(t, imageSize)
	fs, err := Format(img, volumeOffset, volumeSize, Options{InodeCount: 4000})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	if err := fs.WriteFile("etc/passwd", strings.NewReader("root:x:0:0:root:/root:/bin/bash")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := fs.AppendFile("etc/passwd", []byte("\nana:x:1000:1000::/home/ana:/bin/bash\n")); err != nil {
		t.Fatalf("AppendFile: %v", err)
	}

	quiero := "root:x:0:0:root:/root:/bin/bash\nana:x:1000:1000::/home/ana:/bin/bash\n"
	if got := leerFichero(t, fs, "etc/passwd"); got != quiero {
		t.Fatalf("passwd = %q, queria %q", got, quiero)
	}
}

func leerFichero(t *testing.T, fs *FS, path string) string {
	t.Helper()
	rc, _, err := fs.OpenFile(path)
	if err != nil {
		t.Fatalf("abrir %q: %v", path, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("leer %q: %v", path, err)
	}
	return string(body)
}
