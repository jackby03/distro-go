package linuxiso

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// fixture es el ISO de Mint del banco de pruebas. Esta ignorado por git: si no
// esta, los tests que dependen de el se saltan.
const fixture = "../../testdata/isos/linuxmint-22.3-xfce-64bit.iso"

func openFixture(t *testing.T) *ISO {
	t.Helper()
	iso, err := Open(fixture)
	if err != nil {
		t.Skipf("fixture no disponible: %v", err)
	}
	t.Cleanup(func() { _ = iso.Close() })
	return iso
}

func TestFactsFromMintISO(t *testing.T) {
	iso := openFixture(t)

	f, err := iso.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}

	if f.Variant != VariantCasper {
		t.Errorf("variante = %q, se esperaba casper", f.Variant)
	}
	if f.Kernel != "casper/vmlinuz" {
		t.Errorf("kernel = %q", f.Kernel)
	}
	if f.Initrd != "casper/initrd.lz" {
		t.Errorf("initrd = %q", f.Initrd)
	}
	if f.Squashfs != "casper/filesystem.squashfs" {
		t.Errorf("squashfs = %q", f.Squashfs)
	}
	if f.SquashfsSize != 2574745600 {
		t.Errorf("tamano del squashfs = %d, se esperaban 2574745600", f.SquashfsSize)
	}
	if f.Manifest != "casper/filesystem.manifest" {
		t.Errorf("manifest = %q", f.Manifest)
	}
	if f.EFIDir != "EFI/boot" {
		t.Errorf("EFIDir = %q", f.EFIDir)
	}
	if f.GrubCfg != "boot/grub/grub.cfg" {
		t.Errorf("GrubCfg = %q", f.GrubCfg)
	}
	if f.Label != "Linux Mint 22.3 Xfce 64-bit" {
		t.Errorf("etiqueta = %q", f.Label)
	}
}

func TestKernelAndInitrdSizes(t *testing.T) {
	iso := openFixture(t)

	cases := map[string]int64{
		"casper/vmlinuz":       15571336,
		"casper/initrd.lz":     82518462,
		"EFI/boot/bootx64.efi": 966664,
		"EFI/boot/grubx64.efi": 2328456,
		"boot/grub/grub.cfg":   1115,
		"boot/grub/x86_64-efi": 0, // directorio: no se pide tamano
	}
	for name, want := range cases {
		if want == 0 {
			if !iso.Exists(name) {
				t.Errorf("%q deberia existir", name)
			}
			continue
		}
		got, err := iso.SizeOf(name)
		if err != nil {
			t.Errorf("SizeOf(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("SizeOf(%q) = %d, se esperaban %d", name, got, want)
		}
	}
}

// El squashfs se lee en su sitio, sin copiarlo: los primeros bytes tienen que ser
// su magic, y el lector tiene que ser aleatorio.
func TestSquashfsIsReadableInPlace(t *testing.T) {
	iso := openFixture(t)

	ra, size, err := iso.ReaderAt("casper/filesystem.squashfs")
	if err != nil {
		t.Fatalf("ReaderAt: %v", err)
	}
	if size != 2574745600 {
		t.Fatalf("tamano = %d", size)
	}

	magic := make([]byte, 4)
	if _, err := ra.ReadAt(magic, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(magic, []byte("hsqs")) {
		t.Fatalf("magic = %q, se esperaba hsqs", magic)
	}

	// Lectura aleatoria al final del fichero, para probar que no es un flujo.
	tail := make([]byte, 4)
	if _, err := ra.ReadAt(tail, size-4); err != nil {
		t.Fatalf("ReadAt al final: %v", err)
	}
}

func TestLookupIgnoresCase(t *testing.T) {
	iso := openFixture(t)

	lower, err := iso.SizeOf("casper/vmlinuz")
	if err != nil {
		t.Fatalf("SizeOf en minusculas: %v", err)
	}
	upper, err := iso.SizeOf("CASPER/VMLINUZ")
	if err != nil {
		t.Fatalf("SizeOf en mayusculas: %v", err)
	}
	if lower != upper {
		t.Fatalf("tamanos distintos: %d y %d", lower, upper)
	}
}

func TestMissingPaths(t *testing.T) {
	iso := openFixture(t)

	if _, err := iso.SizeOf("no/existe.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, se esperaba ErrNotFound", err)
	}
	if _, err := iso.SizeOf("casper"); !errors.Is(err, ErrIsDir) {
		t.Errorf("error = %v, se esperaba ErrIsDir", err)
	}
	if _, _, err := iso.ReaderAt("casper"); !errors.Is(err, ErrIsDir) {
		t.Errorf("error = %v, se esperaba ErrIsDir", err)
	}
}

func TestOpenFileStreams(t *testing.T) {
	iso := openFixture(t)

	rc, size, err := iso.OpenFile("boot/grub/grub.cfg")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		t.Fatalf("leer: %v", err)
	}
	if int64(len(body)) != size {
		t.Errorf("leidos %d bytes de %d", len(body), size)
	}
	if !bytes.Contains(body, []byte("menuentry")) {
		t.Error("el grub.cfg del ISO deberia tener menuentry")
	}
}

func TestWalkEFI(t *testing.T) {
	iso := openFixture(t)

	var files, dirs int
	var found bool
	err := iso.Walk("EFI", func(e Entry) error {
		if e.IsDir {
			dirs++
			return nil
		}
		files++
		if e.Path == "EFI/boot/bootx64.efi" || e.Path == "EFI/BOOT/BOOTX64.EFI" {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !found {
		t.Error("Walk no encontro el cargador EFI de 64 bits")
	}
	if files == 0 || dirs == 0 {
		t.Errorf("recorrido incompleto: %d ficheros, %d directorios", files, dirs)
	}
}

func TestWalkGrubModules(t *testing.T) {
	iso := openFixture(t)

	mods := 0
	err := iso.Walk("boot/grub/x86_64-efi", func(e Entry) error {
		if !e.IsDir {
			mods++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if mods < 50 {
		t.Errorf("solo %d modulos: el arbol de GRUB deberia ser mucho mayor", mods)
	}
	t.Logf("modulos x86_64-efi en el ISO: %d", mods)
}

// ------------------------------------------------------------------- deteccion

func fakeProbe(files map[string]int64) probe {
	return func(name string) (int64, bool) {
		size, ok := files[name]
		return size, ok
	}
}

func TestDetectCasper(t *testing.T) {
	f, err := detect("test", fakeProbe(map[string]int64{
		"casper/vmlinuz":             100,
		"casper/initrd.lz":           200,
		"casper/filesystem.squashfs": 300,
		"casper/filesystem.manifest": 400,
		"EFI/boot/bootx64.efi":       500,
		"boot/grub/grub.cfg":         600,
	}))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if f.Variant != VariantCasper || f.SquashfsSize != 300 {
		t.Errorf("facts = %+v", f)
	}
	if f.Manifest != "casper/filesystem.manifest" {
		t.Errorf("manifest = %q", f.Manifest)
	}
	if f.EFIDir != "EFI/boot" || f.GrubCfg != "boot/grub/grub.cfg" {
		t.Errorf("rutas de arranque: %+v", f)
	}
}

func TestDetectLiveVariant(t *testing.T) {
	f, err := detect("debian", fakeProbe(map[string]int64{
		"live/vmlinuz":             100,
		"live/initrd.img":          200,
		"live/filesystem.squashfs": 300,
	}))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if f.Variant != VariantLive {
		t.Errorf("variante = %q", f.Variant)
	}
	if f.Initrd != "live/initrd.img" {
		t.Errorf("initrd = %q", f.Initrd)
	}
	if f.EFIDir != "" || f.GrubCfg != "" {
		t.Errorf("no deberia inventar rutas de arranque: %+v", f)
	}
}

func TestDetectWithoutInitrdIsNotLive(t *testing.T) {
	_, err := detect("roto", fakeProbe(map[string]int64{
		"casper/vmlinuz":             100,
		"casper/filesystem.squashfs": 300,
	}))
	if !errors.Is(err, ErrNotLive) {
		t.Fatalf("error = %v, se esperaba ErrNotLive", err)
	}
}

func TestDetectUnknownISO(t *testing.T) {
	_, err := detect("instalador", fakeProbe(map[string]int64{
		"isolinux/vmlinuz": 100,
	}))
	if !errors.Is(err, ErrNotLive) {
		t.Fatalf("error = %v, se esperaba ErrNotLive", err)
	}
}

func TestDetectPrefersCasperOverLive(t *testing.T) {
	f, err := detect("ambiguo", fakeProbe(map[string]int64{
		// Las dos disposiciones presentes: gana la primera de la tabla.
		"casper/vmlinuz":             100,
		"casper/initrd.lz":           200,
		"casper/filesystem.squashfs": 300,
		"live/vmlinuz":               400,
		"live/initrd.img":            500,
		"live/filesystem.squashfs":   600,
	}))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if f.Variant != VariantCasper {
		t.Errorf("variante = %q, se esperaba casper", f.Variant)
	}
}
