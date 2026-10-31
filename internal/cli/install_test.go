package cli

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
)

// TestInstallTargetWritesRootParam es el test que habria cazado el fallo real del
// primer arranque: el parametro del kernel se escribia como UUID=... sin root=,
// y el initramfs se quedaba sin raiz.
func TestInstallTargetWritesRootParam(t *testing.T) {
	const (
		imageSize  = 64 << 20
		espOffset  = 1 << 20
		espSize    = 40 << 20
		rootOffset = espOffset + espSize
		rootSize   = 20 << 20
	)

	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "img"), imageSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	defer img.Close()

	espSection, err := blockdev.NewSection(img, espOffset, espSize)
	if err != nil {
		t.Fatalf("NewSection: %v", err)
	}
	esp, err := fat32.Format(espSection, fat32.Options{Label: "SYSTEM"})
	if err != nil {
		t.Fatalf("fat32.Format: %v", err)
	}

	root, err := ext4fs.Format(img, rootOffset, rootSize, ext4fs.Options{
		Label:      "distrogo-root",
		InodeCount: 2000,
	})
	if err != nil {
		t.Fatalf("ext4fs.Format: %v", err)
	}

	// Un kernel y un initrd falsos, con el nombre que espera la instalacion.
	if err := root.WriteFile("boot/vmlinuz-9.9.9-test", strings.NewReader("kernel")); err != nil {
		t.Fatalf("WriteFile kernel: %v", err)
	}
	if err := root.WriteFile("boot/initrd.img-9.9.9-test", strings.NewReader("initrd")); err != nil {
		t.Fatalf("WriteFile initrd: %v", err)
	}

	rootInfo, err := root.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if _, err := app.installTarget(esp, root, esp.Info(), rootInfo, "Prueba"); err != nil {
		t.Fatalf("installTarget: %v", err)
	}

	// El grub.cfg tiene que arrancar el kernel instalado con root=UUID.
	cfg := readText(t, esp.OpenFile, "boot/grub/grub.cfg")
	for _, want := range []string{
		"root=UUID=" + rootInfo.UUID,
		"/boot/vmlinuz-9.9.9-test",
		"/boot/initrd.img-9.9.9-test",
		"rw",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("el grub.cfg no contiene %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "boot=casper") {
		t.Error("una instalacion real no deberia arrancar en modo live")
	}

	// Y el fstab tiene el root y el ESP, cada uno por su UUID.
	fstab := readText(t, root.OpenFile, "etc/fstab")
	for _, want := range []string{
		"UUID=" + rootInfo.UUID,
		" / ",
		"ext4",
		"UUID=" + esp.Info().UUID(),
		"/boot/efi",
		"vfat",
	} {
		if !strings.Contains(fstab, want) {
			t.Errorf("el fstab no contiene %q:\n%s", want, fstab)
		}
	}

	// Y los dos ficheros del kernel tienen que estar en el ESP.
	for _, path := range []string{"boot/vmlinuz-9.9.9-test", "boot/initrd.img-9.9.9-test"} {
		if _, err := esp.Stat(path); err != nil {
			t.Errorf("%q no esta en el ESP: %v", path, err)
		}
	}
}

// readText lee un fichero del volumen con el abridor que le pasen.
func readText(t *testing.T, open func(string) (io.ReadCloser, int64, error), path string) string {
	t.Helper()
	rc, _, err := open(path)
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
