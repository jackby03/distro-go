package ext4fs

import (
	"errors"
	"io"
	iofs "io/fs"
	"strings"
	"testing"
	"time"

	"distrogo/pkg/rootfs"
)

// source es un arbol en memoria para alimentar Populate.
type source struct {
	entries []rootfs.Entry
	content map[string]string
}

func (s *source) Walk(fn func(rootfs.Entry) error) error {
	for _, e := range s.entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (s *source) Open(path string) (io.ReadCloser, error) {
	body, ok := s.content[path]
	if !ok {
		return nil, errors.New("no existe en el origen")
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func newFS(t *testing.T) *FS {
	t.Helper()
	img := newImage(t, imageSize)
	fs, err := Format(img, volumeOffset, volumeSize, Options{Label: "target", InodeCount: 4000})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}

func TestTargetMkdirAndCreate(t *testing.T) {
	fs := newFS(t)
	tgt := fs.Target()
	when := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	dir := rootfs.Entry{Path: "etc", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when}
	if err := tgt.Mkdir(dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := tgt.Chmod(dir); err != nil {
		t.Fatalf("Chmod del directorio: %v", err)
	}

	file := rootfs.Entry{Path: "etc/motd", Type: rootfs.TypeRegular, Mode: 0o644, Size: 6, ModTime: when}
	w, err := tgt.Create(file)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("hola\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Un setuid de verdad, aplicado despues de escribir.
	sudo := rootfs.Entry{Path: "etc/sudo", Type: rootfs.TypeRegular, Mode: 0o4755 | iofs.ModeSetuid, ModTime: when}
	w2, err := tgt.Create(sudo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _ = w2.Write([]byte("x"))
	if err := w2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tgt.Chmod(sudo); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := tgt.Chown(sudo); err != nil {
		t.Fatalf("Chown: %v", err)
	}
	if err := tgt.SetTimes(sudo); err != nil {
		t.Fatalf("SetTimes: %v", err)
	}

	got, err := fs.efs.ReadFile("etc/motd")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hola\n" {
		t.Errorf("contenido = %q", got)
	}

	fi, err := fs.efs.Stat("etc/sudo")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("permisos = %o", fi.Mode().Perm())
	}
	if fi.Mode()&iofs.ModeSetuid == 0 {
		t.Errorf("se perdio el setuid: mode = %v", fi.Mode())
	}
}

func TestTargetSymlink(t *testing.T) {
	fs := newFS(t)
	tgt := fs.Target()

	if err := tgt.Mkdir(rootfs.Entry{Path: "usr", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755}); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := rootfs.Entry{Path: "bin", Type: rootfs.TypeSymlink, Mode: iofs.ModeSymlink | 0o777, Link: "usr"}
	if err := tgt.Symlink(link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	target, err := fs.efs.ReadLink("bin")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if target != "usr" {
		t.Errorf("el symlink apunta a %q", target)
	}
}

func TestTargetReportsUnsupported(t *testing.T) {
	fs := newFS(t)
	tgt := fs.Target()

	device := rootfs.Entry{Path: "null", Type: rootfs.TypeDevice, Mode: iofs.ModeDevice | 0o666, Rdev: 1<<32 | 3}
	err := tgt.Mknod(device)
	if !errors.Is(err, rootfs.ErrUnsupported) {
		t.Errorf("Mknod = %v, se esperaba ErrUnsupported", err)
	}

	err = tgt.Hardlink(rootfs.Entry{Path: "b", Link: "a"})
	if !errors.Is(err, rootfs.ErrUnsupported) {
		t.Errorf("Hardlink = %v, se esperaba ErrUnsupported", err)
	}
}

// El test que importa: volcar un arbol en un ext4 real con Populate.
func TestPopulateIntoExt4(t *testing.T) {
	fs := newFS(t)
	when := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	src := &source{
		entries: []rootfs.Entry{
			{Path: "etc", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
			{Path: "etc/hostname", Type: rootfs.TypeRegular, Mode: 0o644, Size: 11, ModTime: when},
			{Path: "usr", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
			{Path: "usr/bin", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
			{Path: "usr/bin/sudo", Type: rootfs.TypeRegular, Mode: iofs.ModeSetuid | 0o755, Size: 9, ModTime: when},
			{Path: "usr/lib", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
			{Path: "usr/lib/libc.so", Type: rootfs.TypeSymlink, Mode: iofs.ModeSymlink | 0o777, Link: "libc.so.6", ModTime: when},
			{Path: "bin", Type: rootfs.TypeSymlink, Mode: iofs.ModeSymlink | 0o777, Link: "usr/bin", ModTime: when},
			{Path: "dev", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755, ModTime: when},
			{Path: "dev/null", Type: rootfs.TypeDevice, Mode: iofs.ModeDevice | iofs.ModeCharDevice | 0o666, ModTime: when},
		},
		content: map[string]string{
			"etc/hostname": "distrogo\n",
			"usr/bin/sudo": "SUDO....\n",
		},
	}

	stats, err := rootfs.Populate(src, fs.Target(), rootfs.PopulateOptions{TolerateUnsupported: true})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}

	if stats.Dirs != 5 || stats.Files != 2 || stats.Symlinks != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	// go-diskfs no implementa Mknod: el device node se cuenta, no se falla.
	if stats.Unsupported != 1 || stats.Devices != 0 {
		t.Errorf("unsupported = %d, devices = %d", stats.Unsupported, stats.Devices)
	}

	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Y ahora montarlo otra vez y comprobar que todo esta.
	img := fs.dev
	reopened, err := Open(img, volumeOffset, volumeSize)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()

	body, err := reopened.efs.ReadFile("etc/hostname")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "distrogo\n" {
		t.Errorf("hostname = %q", body)
	}

	for path, want := range map[string]string{
		"bin":             "usr/bin",
		"usr/lib/libc.so": "libc.so.6",
	} {
		got, err := reopened.efs.ReadLink(path)
		if err != nil {
			t.Fatalf("ReadLink(%q): %v", path, err)
		}
		if got != want {
			t.Errorf("%q apunta a %q, se esperaba %q", path, got, want)
		}
	}

	fi, err := reopened.efs.Stat("usr/bin/sudo")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode()&iofs.ModeSetuid == 0 {
		t.Error("el setuid no sobrevivio al volcado")
	}

	info, err := reopened.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.FreeBytes >= volumeSize {
		t.Error("el volumen deberia tener menos espacio libre tras el volcado")
	}
}

func TestMknodModeEncoding(t *testing.T) {
	cases := []struct {
		entry rootfs.Entry
		want  int
	}{
		{rootfs.Entry{Type: rootfs.TypeDevice, Mode: iofs.ModeDevice | iofs.ModeCharDevice | 0o600}, 0o020600},
		{rootfs.Entry{Type: rootfs.TypeDevice, Mode: iofs.ModeDevice | 0o660}, 0o060660},
		{rootfs.Entry{Type: rootfs.TypeFifo, Mode: iofs.ModeNamedPipe | 0o644}, 0o010644},
		{rootfs.Entry{Type: rootfs.TypeSocket, Mode: iofs.ModeSocket | 0o777}, 0o140777},
	}
	for _, c := range cases {
		if got := mknodMode(c.entry); got != c.want {
			t.Errorf("%v: modo = %o, se esperaba %o", c.entry.Type, got, c.want)
		}
	}
}

func TestTargetOnClosedVolumeFails(t *testing.T) {
	fs := newFS(t)
	tgt := fs.Target()
	_ = fs.Close()

	// El volumen ya esta cerrado: cualquier operacion tiene que fallar, no
	// provocar un panic por puntero nulo.
	if err := tgt.Mkdir(rootfs.Entry{Path: "x", Type: rootfs.TypeDir}); err == nil {
		t.Error("crear un directorio con el volumen cerrado deberia fallar")
	}

}

// TestChownSkipsOnlyWhenTheParentAlreadyHasThatOwner comprueba la optimizacion
// que evita dos tercios de los recorridos de ruta en un volcado.
func TestChownSkipsOnlyWhenTheParentAlreadyHasThatOwner(t *testing.T) {
	fs := newFS(t)
	tgt := fs.Target()

	// 1. En la raiz, root:root es lo que ya hay: no hay que tocar el inodo.
	if err := tgt.Mkdir(rootfs.Entry{Path: "usr", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755}); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := tgt.Chown(rootfs.Entry{Path: "usr", Type: rootfs.TypeDir, UID: 0, GID: 0}); err != nil {
		t.Fatalf("Chown usr: %v", err)
	}

	// 2. Un directorio de otro usuario: el hijo hereda su dueno, asi que si
	// queremos root hay que cambiarlo de verdad.
	if err := tgt.Mkdir(rootfs.Entry{Path: "home", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755}); err != nil {
		t.Fatalf("Mkdir home: %v", err)
	}
	if err := tgt.Mkdir(rootfs.Entry{Path: "home/ana", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755}); err != nil {
		t.Fatalf("Mkdir home/ana: %v", err)
	}
	if err := tgt.Chown(rootfs.Entry{Path: "home/ana", Type: rootfs.TypeDir, UID: 1000, GID: 1000}); err != nil {
		t.Fatalf("Chown home/ana: %v", err)
	}

	// Un fichero de root dentro del home de ana: sin Chown se quedaria en
	// 1000:1000, que es el bug que caza esta prueba.
	w, err := tgt.Create(rootfs.Entry{Path: "home/ana/.bashrc", Type: rootfs.TypeRegular, Mode: 0o644})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tgt.Chown(rootfs.Entry{Path: "home/ana/.bashrc", Type: rootfs.TypeRegular, UID: 0, GID: 0}); err != nil {
		t.Fatalf("Chown .bashrc: %v", err)
	}

	duenos := map[string][2]uint32{}
	err = Source(fs, "").Walk(func(e rootfs.Entry) error {
		duenos[e.Path] = [2]uint32{e.UID, e.GID}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if got := duenos["home/ana"]; got != [2]uint32{1000, 1000} {
		t.Fatalf("home/ana = %v, queria 1000:1000", got)
	}
	if got := duenos["home/ana/.bashrc"]; got != [2]uint32{0, 0} {
		t.Fatalf(".bashrc = %v, queria 0:0: el hijo heredo el dueno del padre", got)
	}
}

// TestChmodSkipsTheDefaultMode cubre el otro atajo: si el modo pedido es el que
// la entrada ya tiene al nacer, no hay que recorrer la ruta.
func TestChmodSkipsTheDefaultMode(t *testing.T) {
	fs := newFS(t)
	tgt := fs.Target()

	if err := tgt.Mkdir(rootfs.Entry{Path: "etc", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755}); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Pedir el modo que ya tiene tiene que ser inofensivo...
	if err := tgt.Chmod(rootfs.Entry{Path: "etc", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o755}); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	// ...y pedir otro tiene que aplicarse.
	if err := tgt.Chmod(rootfs.Entry{Path: "etc", Type: rootfs.TypeDir, Mode: iofs.ModeDir | 0o700}); err != nil {
		t.Fatalf("Chmod 0700: %v", err)
	}

	var modo iofs.FileMode
	err := Source(fs, "").Walk(func(e rootfs.Entry) error {
		if e.Path == "etc" {
			modo = e.Mode
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if modo != iofs.ModeDir|0o700 {
		t.Fatalf("modo = %v, queria drwx------", modo)
	}
}
