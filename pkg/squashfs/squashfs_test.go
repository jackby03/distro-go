package squashfs

import (
	"errors"
	"io"
	iofs "io/fs"
	"strings"
	"testing"

	"distrogo/pkg/linuxiso"
	"distrogo/pkg/rootfs"
)

const isoFixture = "../../testdata/isos/linuxmint-22.3-xfce-64bit.iso"

// openFixture abre el squashfs de Mint directamente desde el ISO, sin extraerlo.
func openFixture(t *testing.T) *FS {
	t.Helper()

	iso, err := linuxiso.Open(isoFixture)
	if err != nil {
		t.Skipf("fixture no disponible: %v", err)
	}
	t.Cleanup(func() { _ = iso.Close() })

	facts, err := iso.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	ra, size, err := iso.ReaderAt(facts.Squashfs)
	if err != nil {
		t.Fatalf("ReaderAt: %v", err)
	}
	if size != facts.SquashfsSize {
		t.Fatalf("el lector devuelve %d bytes y el hecho dice %d", size, facts.SquashfsSize)
	}

	fs, err := Open(ra)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return fs
}

var errStop = errors.New("basta")

func TestInfo(t *testing.T) {
	fs := openFixture(t)
	info := fs.Info()

	if info.Version != "4.0" {
		t.Errorf("version = %q", info.Version)
	}
	if info.Compression != "xz" {
		t.Errorf("compresion = %q, se esperaba xz", info.Compression)
	}
	if info.BlockSize != 128<<10 {
		t.Errorf("bloque = %d, se esperaban 128 KiB", info.BlockSize)
	}
	if info.Inodes != 507603 {
		t.Errorf("inodos = %d", info.Inodes)
	}
	if info.Fragments != 14760 {
		t.Errorf("fragmentos = %d", info.Fragments)
	}
	if info.IDCount != 36 {
		t.Errorf("ids = %d", info.IDCount)
	}
	// EXPORTABLE | DUPLICATES, sin xattrs.
	if info.Flags != 0x00C0 {
		t.Errorf("flags = 0x%04X, se esperaba 0x00C0", info.Flags)
	}
}

// El primer recorrido se corta pronto: sirve para comprobar el orden y los tipos
// sin esperar al arbol entero.
func TestWalkOrderAndTypes(t *testing.T) {
	fs := openFixture(t)

	var seen []rootfs.Entry
	err := fs.Walk(func(e rootfs.Entry) error {
		if len(seen) >= 400 {
			return errStop
		}
		seen = append(seen, e)
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("el recorrido deberia cortarse, error = %v", err)
	}
	if len(seen) != 400 {
		t.Fatalf("entradas = %d", len(seen))
	}

	dirs := map[string]bool{}
	for _, e := range seen {
		if e.Path == "" || e.Path == "." || strings.HasPrefix(e.Path, "./") {
			t.Fatalf("ruta rara: %q", e.Path)
		}
		if e.Inode == 0 {
			t.Errorf("%q sin numero de inodo", e.Path)
		}
		if e.UID != 0 || e.GID != 0 {
			t.Errorf("%q con uid/gid %d/%d", e.Path, e.UID, e.GID)
		}

		// Un hijo solo puede aparecer despues de su padre.
		if dir := parentPath(e.Path); dir != "" && !dirs[dir] {
			t.Errorf("%q aparece antes que su directorio %q", e.Path, dir)
		}
		if e.Type == rootfs.TypeDir {
			dirs[e.Path] = true
		}
	}
}

func TestReadKnownFile(t *testing.T) {
	fs := openFixture(t)

	// etc/os-release es un symlink en Mint; el fichero real es usr/lib/os-release.
	rc, err := fs.Open("usr/lib/os-release")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("leer: %v", err)
	}
	if !strings.Contains(string(body), "Linux Mint 22.3") {
		t.Fatalf("el contenido no parece el os-release de Mint:\n%s", body)
	}
}

// Una ruta anidada es la que rompe filepath.Clean en Windows: si esto pasa, la
// navegacion componente a componente funciona.
func TestReadNestedPath(t *testing.T) {
	fs := openFixture(t)

	rc, err := fs.Open("usr/lib/os-release")
	if err != nil {
		t.Skipf("usr/lib/os-release no existe en esta imagen: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("leer: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("el fichero esta vacio")
	}
}

func TestSymlinkTarget(t *testing.T) {
	fs := openFixture(t)

	var found bool
	err := fs.Walk(func(e rootfs.Entry) error {
		if e.Path != "bin" {
			return nil
		}
		found = true
		if e.Type != rootfs.TypeSymlink {
			t.Errorf("bin deberia ser un symlink, es %v", e.Type)
		}
		if e.Link != "usr/bin" {
			t.Errorf("bin apunta a %q, se esperaba usr/bin", e.Link)
		}
		if e.Mode&0o777 != 0o777 {
			t.Errorf("permisos de bin = %o", e.Mode&0o777)
		}
		return errStop
	})
	if !errors.Is(err, errStop) && err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !found {
		t.Fatal("no se encontro bin en el arbol")
	}
}

// Los ficheros de cero bytes son el tercer bug de la libreria: al abrirlos
// devuelve "invalid block index". Aqui tienen que leerse como vacios.
func TestZeroByteFilesReadAsEmpty(t *testing.T) {
	fs := openFixture(t)

	var zero []string
	err := fs.Walk(func(e rootfs.Entry) error {
		if e.Type == rootfs.TypeRegular && e.Size == 0 {
			zero = append(zero, e.Path)
			if len(zero) >= 4 {
				return errStop
			}
		}
		return nil
	})
	if !errors.Is(err, errStop) && err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(zero) == 0 {
		t.Skip("esta imagen no tiene ficheros vacios")
	}

	for _, p := range zero {
		rc, err := fs.Open(p)
		if err != nil {
			t.Errorf("Open(%q): %v", p, err)
			continue
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Errorf("leer %q: %v", p, err)
		}
		if len(body) != 0 {
			t.Errorf("%q deberia estar vacio, tiene %d bytes", p, len(body))
		}
	}
}

func TestOpenErrors(t *testing.T) {
	fs := openFixture(t)

	if _, err := fs.Open("no/existe.txt"); err == nil {
		t.Error("abrir algo inexistente deberia fallar")
	}
	if _, err := fs.Open("etc"); err == nil {
		t.Error("abrir un directorio deberia fallar")
	}
	// etc/os-release es un symlink: no tiene contenido propio.
	if _, err := fs.Open("etc/os-release"); err == nil {
		t.Error("abrir un symlink deberia fallar")
	}
	if _, err := fs.Open("etc/no-existe"); err == nil {
		t.Error("abrir algo inexistente dentro de un directorio deberia fallar")
	}
}

// Los hardlinks son inodos distintos con el mismo numero: el recorrido tiene que
// reportarlos con nlink mayor que uno.
func TestHardlinksAreVisible(t *testing.T) {
	fs := openFixture(t)

	inodes := map[uint64]uint32{}
	err := fs.Walk(func(e rootfs.Entry) error {
		if e.Type != rootfs.TypeRegular {
			return nil
		}
		if e.Nlink > 1 {
			inodes[e.Inode] = e.Nlink
			if len(inodes) >= 3 {
				return errStop
			}
		}
		return nil
	})
	if !errors.Is(err, errStop) && err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(inodes) == 0 {
		t.Fatal("no se encontro ningun hardlink")
	}
	for inode, nlink := range inodes {
		if nlink < 2 {
			t.Errorf("inodo %d con nlink %d", inode, nlink)
		}
	}
}

// Recorrido completo. Tarda alrededor de dos minutos, asi que se salta con -short.
func TestWalkWholeTree(t *testing.T) {
	if testing.Short() {
		t.Skip("el arbol completo tarda demasiado para -short")
	}
	fs := openFixture(t)

	counts := map[rootfs.Type]int{}
	var total int
	var setuid, setgid int
	var maxName int

	err := fs.Walk(func(e rootfs.Entry) error {
		total++
		counts[e.Type]++
		if e.Mode&iofs.ModeSetuid != 0 {
			setuid++
		}
		if e.Mode&iofs.ModeSetgid != 0 {
			setgid++
		}
		if len(e.Path) > maxName {
			maxName = len(e.Path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	// Cifras medidas con el ISO de Mint 22.3 Xfce.
	want := map[rootfs.Type]int{
		rootfs.TypeRegular: 322477,
		rootfs.TypeSymlink: 162239,
		rootfs.TypeDir:     22899,
		rootfs.TypeDevice:  8,
	}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%v: %d entradas, se esperaban %d", kind, counts[kind], n)
		}
	}
	if total != 507623 {
		t.Errorf("total %d entradas, se esperaban 507623", total)
	}
	if setuid != 18 || setgid != 18 {
		t.Errorf("setuid/setgid = %d/%d, se esperaban 18/18", setuid, setgid)
	}
	if maxName < 100 {
		t.Errorf("la ruta mas larga mide %d, se esperaba mas", maxName)
	}
}

func parentPath(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return ""
}
