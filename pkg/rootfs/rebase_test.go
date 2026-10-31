package rootfs

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
)

// skelSource imita un /etc/skel minimo.
func skelSource() *fakeSource {
	return &fakeSource{
		entries: []Entry{
			{Path: ".bashrc", Type: TypeRegular, Mode: 0o644, UID: 0, GID: 0, Size: 12},
			{Path: ".profile", Type: TypeRegular, Mode: 0o644, UID: 0, GID: 0, Size: 20},
			{Path: ".config", Type: TypeDir, Mode: 0o755, UID: 0, GID: 0},
			{Path: ".config/monitors.xml", Type: TypeRegular, Mode: 0o600, UID: 1000, GID: 1000, Size: 5},
		},
		content: map[string]string{
			".bashrc":              "export PS1=x",
			".profile":             "# profile\n",
			".config/monitors.xml": "<xml/>",
		},
	}
}

func paths(src Source) ([]string, error) {
	var out []string
	err := src.Walk(func(e Entry) error {
		out = append(out, e.Path)
		return nil
	})
	return out, err
}

func TestRebasePrefixesEveryPath(t *testing.T) {
	got, err := paths(Rebase(skelSource(), "/home/ana"))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	quiere := []string{
		"home/ana/.bashrc",
		"home/ana/.profile",
		"home/ana/.config",
		"home/ana/.config/monitors.xml",
	}
	if strings.Join(got, ",") != strings.Join(quiere, ",") {
		t.Fatalf("rutas = %v, queria %v", got, quiere)
	}
}

func TestRebaseOpenMapsBackToTheSource(t *testing.T) {
	src := Rebase(skelSource(), "/home/ana")

	rc, err := src.Open("/home/ana/.bashrc")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if string(body) != "export PS1=x" {
		t.Fatalf("contenido = %q", string(body))
	}
}

func TestRebaseRejectsPathsOutsideTheSubtree(t *testing.T) {
	src := Rebase(skelSource(), "/home/ana")

	for _, ruta := range []string{
		"/etc/passwd",
		"/home/otro/.bashrc",
		"/home/ana",            // la raiz no la sirve el subarbol
		"/home/anabel/.bashrc", // ojo: no basta con el prefijo de texto
		"",
	} {
		if _, err := src.Open(ruta); !errors.Is(err, ErrOutside) {
			t.Errorf("%q: err = %v, queria ErrOutside", ruta, err)
		}
	}
}

func TestRebaseWithoutPrefixIsTheSameTree(t *testing.T) {
	original := skelSource()

	for _, prefijo := range []string{"", "/"} {
		src := Rebase(original, prefijo)
		got, err := paths(src)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if strings.Join(got, ",") != ".bashrc,.profile,.config,.config/monitors.xml" {
			t.Fatalf("prefijo %q: rutas = %v", prefijo, got)
		}
	}
}

func TestRebaseToleratesSlashEdges(t *testing.T) {
	for _, prefijo := range []string{"/home/ana", "home/ana/", "/home/ana/"} {
		src := Rebase(skelSource(), prefijo)

		got, err := paths(src)
		if err != nil {
			t.Fatalf("prefijo %q: %v", prefijo, err)
		}
		if len(got) == 0 || got[0] != "home/ana/.bashrc" {
			t.Fatalf("prefijo %q: primera ruta = %q", prefijo, got)
		}
		if _, err := src.Open("/home/ana/.profile"); err != nil {
			t.Fatalf("prefijo %q: no se pudo abrir: %v", prefijo, err)
		}
	}
}

func TestRebaseSkipsTheSubtreeRootEntry(t *testing.T) {
	// Si el origen entrega su propia raiz, no debe llegar al destino: la crea
	// quien llama, con el dueno del usuario y no el de /etc/skel.
	conRaiz := &fakeSource{
		entries: []Entry{
			{Path: "", Type: TypeDir, Mode: 0o755},
			{Path: "/", Type: TypeDir, Mode: 0o755},
			{Path: ".bashrc", Type: TypeRegular, Mode: 0o644},
		},
		content: map[string]string{".bashrc": "x"},
	}

	got, err := paths(Rebase(conRaiz, "/home/ana"))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if strings.Join(got, ",") != "home/ana/.bashrc" {
		t.Fatalf("rutas = %v, no deberia incluir la raiz del subarbol", got)
	}
}

func TestRebaseKeepsInodesSoHardlinksStillMatch(t *testing.T) {
	src := Rebase(&fakeSource{entries: []Entry{
		{Path: "a", Type: TypeRegular, Inode: 42, Nlink: 2},
		{Path: "b", Type: TypeRegular, Inode: 42, Nlink: 2},
	}}, "/home/ana")

	var inodos []uint64
	var nlinks []uint32
	err := src.Walk(func(e Entry) error {
		inodos = append(inodos, e.Inode)
		nlinks = append(nlinks, e.Nlink)
		return nil
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(inodos) != 2 || inodos[0] != 42 || inodos[1] != 42 {
		t.Fatalf("inodos = %v, el rebase no debe tocarlos", inodos)
	}
	if nlinks[0] != 2 || nlinks[1] != 2 {
		t.Fatalf("nlinks = %v", nlinks)
	}
}

func TestPopulateOverRebaseWritesUnderThePrefix(t *testing.T) {
	tgt := newFakeTarget()

	stats, err := Populate(Rebase(skelSource(), "/home/ana"), tgt, PopulateOptions{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if stats.Files != 3 || stats.Dirs != 1 {
		t.Fatalf("stats = %+v, queria 3 ficheros y 1 dir", stats)
	}

	if _, ok := tgt.content["home/ana/.bashrc"]; !ok {
		t.Fatalf("no se escribio el fichero bajo el prefijo: %v", tgt.content)
	}
	if _, ok := tgt.content["home/ana/.config/monitors.xml"]; !ok {
		t.Fatalf("no se escribio el fichero anidado: %v", tgt.content)
	}
	if tgt.modes["home/ana/.config/monitors.xml"] != 0o600 {
		t.Fatalf("mode = %v, queria 0600", tgt.modes["home/ana/.config/monitors.xml"])
	}
	if tgt.owners["home/ana/.config/monitors.xml"] != [2]uint32{1000, 1000} {
		t.Fatalf("dueno = %v, queria 1000:1000", tgt.owners["home/ana/.config/monitors.xml"])
	}
	// Y la raiz del subarbol no se toca, porque no viene del origen.
	for _, op := range tgt.ops {
		if op == "mkdir home/ana" || op == "chown home/ana" || op == "chmod home/ana" {
			t.Fatalf("el populate toco la raiz del subarbol: %s", op)
		}
	}
}

func TestRebaseTargetKeepsTypeModes(t *testing.T) {
	// Un symlink de skel (por ejemplo .config/autostart/x.desktop) tiene que
	// seguir siendo symlink despues del rebase.
	src := Rebase(&fakeSource{entries: []Entry{
		{Path: "link", Type: TypeSymlink, Link: "/usr/bin/x", Mode: fs.ModeSymlink | 0o777},
	}}, "/home/ana")

	var visto *Entry
	err := src.Walk(func(e Entry) error {
		copia := e
		visto = &copia
		return nil
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if visto == nil || visto.Type != TypeSymlink || visto.Link != "/usr/bin/x" {
		t.Fatalf("el symlink se perdio en el rebase: %+v", visto)
	}
}
