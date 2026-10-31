package ext4fs

import (
	"io"
	iofs "io/fs"
	"strings"
	"testing"
	"time"

	"distrogo/pkg/rootfs"
)

// escribir arbol en el volumen usando el propio Target, que es como llega el
// contenido en una instalacion real.
func escribir(t *testing.T, fs *FS, entries []rootfs.Entry, content map[string]string) {
	t.Helper()

	tgt := fs.Target()
	for _, e := range entries {
		var err error
		switch e.Type {
		case rootfs.TypeDir:
			err = tgt.Mkdir(e)
		case rootfs.TypeSymlink:
			err = tgt.Symlink(e)
		case rootfs.TypeRegular:
			var w io.WriteCloser
			w, err = tgt.Create(e)
			if err == nil {
				if _, cerr := io.WriteString(w, content[e.Path]); cerr != nil {
					err = cerr
				}
				if cerr := w.Close(); err == nil {
					err = cerr
				}
			}
		}
		if err != nil {
			t.Fatalf("escribir %q: %v", e.Path, err)
		}
		if e.Type != rootfs.TypeSymlink {
			if err := tgt.Chmod(e); err != nil {
				t.Fatalf("chmod %q: %v", e.Path, err)
			}
			if err := tgt.Chown(e); err != nil {
				t.Fatalf("chown %q: %v", e.Path, err)
			}
		}
	}
}

// montarSkel deja un /etc/skel de pruebas en el volumen.
func montarSkel(t *testing.T) *FS {
	t.Helper()
	fs := newFS(t)
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	escribir(t, fs, []rootfs.Entry{
		{Path: "etc", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
		{Path: "etc/skel", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
		{Path: "etc/skel/.bashrc", Type: rootfs.TypeRegular, Mode: 0o644, UID: 0, GID: 0, ModTime: when},
		{Path: "etc/skel/.profile", Type: rootfs.TypeRegular, Mode: 0o644, UID: 0, GID: 0, ModTime: when},
		{Path: "etc/skel/.config", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o700, UID: 1000, GID: 1000, ModTime: when},
		{Path: "etc/skel/.config/monitors.xml", Type: rootfs.TypeRegular, Mode: 0o600, UID: 1000, GID: 1000, ModTime: when},
		{Path: "etc/skel/.local", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
		{Path: "etc/skel/.local/bin", Type: rootfs.TypeSymlink, Link: "../bin", ModTime: when},
	}, map[string]string{
		"etc/skel/.bashrc":              "export PS1=skel\n",
		"etc/skel/.profile":             "# skel\n",
		"etc/skel/.config/monitors.xml": "<monitors/>",
	})
	return fs
}

func recorrer(t *testing.T, src rootfs.Source) map[string]rootfs.Entry {
	t.Helper()

	got := map[string]rootfs.Entry{}
	var orden []string
	err := src.Walk(func(e rootfs.Entry) error {
		got[e.Path] = e
		orden = append(orden, e.Path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// Los padres tienen que ir antes que los hijos.
	for i, ruta := range orden {
		if idx := strings.LastIndex(ruta, "/"); idx >= 0 {
			padre := ruta[:idx]
			encontrado := false
			for _, previa := range orden[:i] {
				if previa == padre {
					encontrado = true
					break
				}
			}
			if !encontrado {
				t.Fatalf("el hijo %q aparece antes que su padre %q: %v", ruta, padre, orden)
			}
		}
	}
	return got
}

func TestSourceWalksRelativePaths(t *testing.T) {
	src := Source(montarSkel(t), "etc/skel")
	got := recorrer(t, src)

	quiere := []string{
		".bashrc", ".profile", ".config", ".config/monitors.xml", ".local", ".local/bin",
	}
	for _, ruta := range quiere {
		if _, ok := got[ruta]; !ok {
			t.Fatalf("falta %q en %v", ruta, quiere)
		}
	}
	if len(got) != len(quiere) {
		t.Fatalf("entradas = %d, queria %d", len(got), len(quiere))
	}
}

func TestSourceKeepsModesOwnersAndTypes(t *testing.T) {
	src := Source(montarSkel(t), "etc/skel")
	got := recorrer(t, src)

	casos := []struct {
		ruta string
		tipo rootfs.Type
		mode iofs.FileMode
		uid  uint32
		gid  uint32
	}{
		{".bashrc", rootfs.TypeRegular, 0o644, 0, 0},
		{".config", rootfs.TypeDir, iofs.ModeDir | 0o700, 1000, 1000},
		{".config/monitors.xml", rootfs.TypeRegular, 0o600, 1000, 1000},
		{".local/bin", rootfs.TypeSymlink, iofs.ModeSymlink | 0o777, 0, 0},
	}

	for _, c := range casos {
		e, ok := got[c.ruta]
		if !ok {
			t.Fatalf("falta %q", c.ruta)
		}
		if e.Type != c.tipo {
			t.Errorf("%s: tipo = %v, queria %v", c.ruta, e.Type, c.tipo)
		}
		if e.Mode != c.mode {
			t.Errorf("%s: modo = %v, queria %v", c.ruta, e.Mode, c.mode)
		}
		if c.tipo == rootfs.TypeSymlink {
			continue
		}
		if e.UID != c.uid || e.GID != c.gid {
			t.Errorf("%s: dueno = %d:%d, queria %d:%d", c.ruta, e.UID, e.GID, c.uid, c.gid)
		}
	}
}

func TestSourceReadsSymlinkTarget(t *testing.T) {
	src := Source(montarSkel(t), "etc/skel")
	got := recorrer(t, src)

	e, ok := got[".local/bin"]
	if !ok {
		t.Fatal("falta el symlink")
	}
	if e.Link != "../bin" {
		t.Fatalf("destino = %q, queria ../bin", e.Link)
	}
}

func TestSourceKeepsSetuidBits(t *testing.T) {
	// Si go-diskfs perdiera el bit al escribir o al leer, aqui se veria: es el
	// mismo viaje de ida y vuelta que hace una instalacion real.
	//
	// Ojo: en Go el setuid no es el digito octal 4000, es el bit alto
	// os.ModeSetuid. Escribir 0o4755 pondria un bit en zona muerta y no pasaria
	// nada, que es justo el error que buscaba esta prueba.
	fs := newFS(t)
	setuid := iofs.FileMode(0o755) | iofs.ModeSetuid
	escribir(t, fs, []rootfs.Entry{
		{Path: "su", Type: rootfs.TypeRegular, Mode: setuid, UID: 0, GID: 0},
	}, map[string]string{"su": "binario"})

	got := recorrer(t, Source(fs, ""))
	e, ok := got["su"]
	if !ok {
		t.Fatal("falta el fichero")
	}
	if e.Mode != setuid {
		t.Fatalf("modo = %v, queria %v", e.Mode, setuid)
	}
}

func TestSourceOpensContent(t *testing.T) {
	src := Source(montarSkel(t), "etc/skel")

	rc, err := src.Open(".config/monitors.xml")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("leer: %v", err)
	}
	if string(body) != "<monitors/>" {
		t.Fatalf("contenido = %q", string(body))
	}
}

func TestSourceFailsWhenVolumeClosed(t *testing.T) {
	fs := montarSkel(t)
	src := Source(fs, "etc/skel")
	if err := fs.Close(); err != nil {
		t.Fatalf("cerrar: %v", err)
	}

	if err := src.Walk(func(rootfs.Entry) error { return nil }); err == nil {
		t.Fatal("un volumen cerrado no puede recorrerse")
	}
	if _, err := src.Open(".bashrc"); err == nil {
		t.Fatal("un volumen cerrado no puede abrirse")
	}
}

func TestSkelToHomeRoundTrip(t *testing.T) {
	// El caso real: /etc/skel se copia a /home/ana con Populate y Rebase.
	fs := montarSkel(t)

	if err := fs.Mkdir("home"); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := fs.Mkdir("home/ana"); err != nil {
		t.Fatalf("mkdir home/ana: %v", err)
	}
	// El home es del usuario, no de skel.
	escribir(t, fs, []rootfs.Entry{
		{Path: "home", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755},
		{Path: "home/ana", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, UID: 1000, GID: 1000},
	}, map[string]string{})

	stats, err := rootfs.Populate(
		rootfs.Rebase(Source(fs, "etc/skel"), "home/ana"),
		fs.Target(),
		rootfs.PopulateOptions{},
	)
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if stats.Files != 3 || stats.Symlinks != 1 || stats.Dirs != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	got := recorrer(t, Source(fs, "home/ana"))

	if e := got[".bashrc"]; e.Mode != 0o644 || e.UID != 0 || e.GID != 0 {
		t.Fatalf(".bashrc quedo como %v %d:%d", e.Mode, e.UID, e.GID)
	}
	if e := got[".config/monitors.xml"]; e.Mode != 0o600 || e.UID != 1000 || e.GID != 1000 {
		t.Fatalf("monitors.xml quedo como %v %d:%d", e.Mode, e.UID, e.GID)
	}
	if e := got[".local/bin"]; e.Type != rootfs.TypeSymlink || e.Link != "../bin" {
		t.Fatalf("el symlink quedo como %+v", e)
	}

	// Y el directorio del home sigue siendo del usuario, no de root.
	propio, err := fs.efs.Stat("home/ana")
	if err != nil {
		t.Fatalf("stat del home: %v", err)
	}
	eHome := entryFromFileInfo(propio, "ana")
	if eHome.UID != 1000 || eHome.GID != 1000 {
		t.Fatalf("el populate cambio el dueno del home: %d:%d", eHome.UID, eHome.GID)
	}
	if eHome.Type != rootfs.TypeDir {
		t.Fatalf("el home deberia seguir siendo un directorio: %v", eHome.Type)
	}
}
