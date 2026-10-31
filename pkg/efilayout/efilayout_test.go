package efilayout

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/fat32"
	"distrogo/pkg/linuxiso"
)

const fixture = "../../testdata/isos/linuxmint-22.3-xfce-64bit.iso"

// 300 MiB dan 76800 clusters de 4 KiB: suficiente para FAT32 y para los ~104 MB
// de artefactos de arranque de Mint.
const volumeSize = 300 << 20

func openFixture(t *testing.T) *linuxiso.ISO {
	t.Helper()
	iso, err := linuxiso.Open(fixture)
	if err != nil {
		t.Skipf("fixture no disponible: %v", err)
	}
	t.Cleanup(func() { _ = iso.Close() })
	return iso
}

func newVolume(t *testing.T) (*blockdev.Image, *fat32.FileSystem) {
	t.Helper()
	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "esp.img"), volumeSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	t.Cleanup(func() { _ = img.Close() })

	fs, err := fat32.Format(img, fat32.Options{Label: "EFI"})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	return img, fs
}

func TestGrubCfg(t *testing.T) {
	cfg := GrubCfg("Mint live",
		[]string{"boot=casper", "persistent", "quiet", ""},
		"casper/vmlinuz", "/casper/initrd.lz")

	for _, want := range []string{
		`menuentry "Mint live" {`,
		"linux /casper/vmlinuz boot=casper persistent quiet\n",
		"initrd /casper/initrd.lz",
		"set timeout=3",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("falta %q en el grub.cfg:\n%s", want, cfg)
		}
	}
	// Un argumento vacio no debe dejar un espacio de mas.
	if strings.Contains(cfg, "quiet  \n") {
		t.Error("la linea del kernel tiene un espacio de mas")
	}
}

func TestGrubCfgEscapesTheTitle(t *testing.T) {
	cfg := GrubCfg(`Mint "22.3"`, nil, "casper/vmlinuz", "casper/initrd.lz")

	if !strings.Contains(cfg, `menuentry "Mint \"22.3\"" {`) {
		t.Errorf("el titulo no esta escapado:\n%s", cfg)
	}
}

func TestWriteCopiesBootArtifacts(t *testing.T) {
	if testing.Short() {
		t.Skip("copia unos 104 MB del ISO real")
	}
	iso := openFixture(t)
	img, esp := newVolume(t)

	facts, err := iso.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}

	res, err := Write(iso, esp, Spec{
		Title:      "Linux Mint live",
		KernelArgs: []string{"boot=casper", "persistent", "quiet", "splash"},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := esp.Close(); err != nil {
		t.Fatalf("Close del volumen: %v", err)
	}

	// 3 ficheros en EFI/boot, 261 modulos, kernel, initrd y el grub.cfg.
	if res.Files < 260 {
		t.Errorf("solo se copiaron %d ficheros", res.Files)
	}
	if minBytes := int64(90 << 20); res.Bytes < minBytes {
		t.Errorf("se copiaron %d bytes, se esperaban al menos %d", res.Bytes, minBytes)
	}

	// Volver a montar desde el disco y comprobar el contenido real.
	disk, err := fat32.Open(img)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	kernel, err := disk.Stat(facts.Kernel)
	if err != nil {
		t.Fatalf("Stat del kernel: %v", err)
	}
	if want, _ := iso.SizeOf(facts.Kernel); kernel.Size != want {
		t.Errorf("el kernel ocupa %d bytes, se esperaban %d", kernel.Size, want)
	}
	initrd, err := disk.Stat(facts.Initrd)
	if err != nil {
		t.Fatalf("Stat del initrd: %v", err)
	}
	if want, _ := iso.SizeOf(facts.Initrd); initrd.Size != want {
		t.Errorf("el initrd ocupa %d bytes, se esperaban %d", initrd.Size, want)
	}

	// Un modulo de nombre largo tiene que estar, con su nombre exacto.
	if _, err := disk.Stat("boot/grub/x86_64-efi/normal.mod"); err != nil {
		t.Errorf("falta normal.mod: %v", err)
	}

	cfg, err := readAll(disk, grubCfgPath)
	if err != nil {
		t.Fatalf("leer el grub.cfg: %v", err)
	}
	if !strings.Contains(string(cfg), "linux /casper/vmlinuz boot=casper persistent quiet splash") {
		t.Errorf("el grub.cfg no arranca el kernel como se esperaba:\n%s", cfg)
	}

	// Y el contenido de un binario EFI tiene que coincidir byte a byte con el ISO.
	if err := sameAsISO(t, iso, disk, "EFI/boot/bootx64.efi"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteSkipsSquashfsByDefault(t *testing.T) {
	iso := openFixture(t)
	_, esp := newVolume(t)

	facts, err := iso.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if _, err := Write(iso, esp, Spec{SkipGrubModules: true}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := esp.Stat(facts.Squashfs); !errors.Is(err, fat32.ErrNotFoundInVolume) {
		t.Fatalf("el squashfs no deberia copiarse por defecto, error = %v", err)
	}
	if _, err := esp.Stat("boot/grub/x86_64-efi/normal.mod"); !errors.Is(err, fat32.ErrNotFoundInVolume) {
		t.Fatalf("con SkipGrubModules no deberia copiarse el arbol de modulos, error = %v", err)
	}
}

func TestWriteFailsWhenTheVolumeIsTooSmall(t *testing.T) {
	iso := openFixture(t)

	small, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "small.img"), 40<<20)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	defer small.Close()

	esp, err := fat32.Format(small, fat32.Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	_, err = Write(iso, esp, Spec{})
	if !errors.Is(err, ErrNotEnoughSpace) {
		t.Fatalf("error = %v, se esperaba ErrNotEnoughSpace", err)
	}

	// Y no debe haber dejado el grub.cfg escrito: el fallo es previo.
	if _, err := esp.Stat(grubCfgPath); !errors.Is(err, fat32.ErrNotFoundInVolume) {
		t.Errorf("no deberia existir grub.cfg tras el fallo, error = %v", err)
	}
}

func readAll(fs *fat32.FileSystem, path string) ([]byte, error) {
	rc, _, err := fs.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// sameAsISO compara un fichero del ESP con su origen en el ISO.
func sameAsISO(t *testing.T, iso *linuxiso.ISO, disk *fat32.FileSystem, path string) error {
	t.Helper()

	fromISO, _, err := iso.OpenFile(path)
	if err != nil {
		return err
	}
	defer fromISO.Close()

	inVolume, _, err := disk.OpenFile(path)
	if err != nil {
		return err
	}
	defer inVolume.Close()

	a, err := io.ReadAll(fromISO)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(inVolume)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New(path + ": el contenido no coincide con el ISO")
	}
	return nil
}

// Fidelidad de nombres: lo que el ISO llama de una forma, el ESP lo conserva.
//
// No se puede exigir mas de lo que trae el ISO: xorriso omite la entrada Rock
// Ridge cuando el nombre solo difiere en mayusculas, asi que algunos modulos de
// GRUB llegan ya en mayusculas desde el ISO. GRUB y el firmware comparan sin
// distinguir caso en FAT, asi que es correcto; el nombre original solo vive en
// el arbol Joliet, que nuestra libreria no lee (DG-25).
func TestNamesMatchTheISOTree(t *testing.T) {
	if testing.Short() {
		t.Skip("copia unos 104 MB del ISO real")
	}
	iso := openFixture(t)
	img, esp := newVolume(t)

	if _, err := Write(iso, esp, Spec{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := esp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk, err := fat32.Open(img)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	checked := 0
	for _, root := range []string{"EFI", "boot/grub/x86_64-efi"} {
		err := iso.Walk(root, func(e linuxiso.Entry) error {
			if e.IsDir {
				return nil
			}
			// El lector del ISO puede devolver bytes de mas tras un NUL (DG-26).
			clean := strings.Split(e.Path, "\x00")[0]
			st, err := disk.Stat(clean)
			if err != nil {
				return fmt.Errorf("falta %q en el ESP: %w", clean, err)
			}
			if want := path.Base(clean); st.Name != want {
				return fmt.Errorf("%q se copio como %q", e.Path, st.Name)
			}
			checked++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if checked < 260 {
		t.Fatalf("solo se comprobaron %d ficheros", checked)
	}
}
