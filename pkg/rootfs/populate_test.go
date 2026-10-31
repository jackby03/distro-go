package rootfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// fakeSource es un arbol en memoria.
type fakeSource struct {
	entries []Entry
	content map[string]string
}

func (s *fakeSource) Walk(fn func(Entry) error) error {
	for _, e := range s.entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeSource) Open(path string) (io.ReadCloser, error) {
	body, ok := s.content[path]
	if !ok {
		return nil, fmt.Errorf("%q no existe en el origen", path)
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// fakeTarget registra lo que le piden, en orden.
type fakeTarget struct {
	ops         []string
	content     map[string]string
	modes       map[string]fs.FileMode
	owners      map[string][2]uint32
	times       map[string]time.Time
	links       map[string]string
	unsupported map[string]bool
}

func newFakeTarget() *fakeTarget {
	return &fakeTarget{
		content:     map[string]string{},
		modes:       map[string]fs.FileMode{},
		owners:      map[string][2]uint32{},
		times:       map[string]time.Time{},
		links:       map[string]string{},
		unsupported: map[string]bool{},
	}
}

func (t *fakeTarget) record(op, path string) error {
	t.ops = append(t.ops, op+" "+path)
	if t.unsupported[op] {
		return fmt.Errorf("%s: %w", op, ErrUnsupported)
	}
	return nil
}

func (t *fakeTarget) Mkdir(e Entry) error { return t.record("mkdir", e.Path) }
func (t *fakeTarget) Chmod(e Entry) error {
	t.modes[e.Path] = e.Mode
	return t.record("chmod", e.Path)
}
func (t *fakeTarget) Chown(e Entry) error {
	t.owners[e.Path] = [2]uint32{e.UID, e.GID}
	return t.record("chown", e.Path)
}
func (t *fakeTarget) SetTimes(e Entry) error {
	t.times[e.Path] = e.ModTime
	return t.record("settimes", e.Path)
}
func (t *fakeTarget) Symlink(e Entry) error {
	t.links[e.Path] = e.Link
	return t.record("symlink", e.Path)
}
func (t *fakeTarget) Hardlink(e Entry) error {
	t.links[e.Path] = e.Link
	return t.record("hardlink", e.Path)
}
func (t *fakeTarget) Mknod(e Entry) error { return t.record("mknod", e.Path) }

func (t *fakeTarget) Create(e Entry) (io.WriteCloser, error) {
	if err := t.record("create", e.Path); err != nil {
		return nil, err
	}
	return &fakeWriter{t: t, path: e.Path}, nil
}

type fakeWriter struct {
	t    *fakeTarget
	path string
	buf  strings.Builder
}

func (w *fakeWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *fakeWriter) Close() error {
	w.t.content[w.path] = w.buf.String()
	w.t.ops = append(w.t.ops, "close "+w.path)
	return nil
}

// ---------------------------------------------------------------- los tests

func testTree() *fakeSource {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &fakeSource{
		entries: []Entry{
			{Path: "etc", Type: TypeDir, Mode: 0o755, UID: 0, GID: 0, ModTime: when},
			{Path: "etc/motd", Type: TypeRegular, Mode: 0o644, Size: 11, ModTime: when},
			{Path: "usr", Type: TypeDir, Mode: 0o755, ModTime: when},
			{Path: "usr/bin", Type: TypeDir, Mode: 0o755, ModTime: when},
			{Path: "usr/bin/sudo", Type: TypeRegular, Mode: 0o4755 | fs.ModeSetuid, Size: 8, ModTime: when},
			{Path: "usr/bin/sudo2", Type: TypeRegular, Mode: 0o4755, Nlink: 2, Inode: 77, Size: 5, ModTime: when},
			{Path: "usr/bin/sudo3", Type: TypeRegular, Mode: 0o4755, Nlink: 2, Inode: 77, ModTime: when},
			{Path: "bin", Type: TypeSymlink, Mode: 0o777, Link: "usr/bin", ModTime: when},
			{Path: "dev", Type: TypeDir, Mode: 0o755, ModTime: when},
			{Path: "dev/null", Type: TypeDevice, Mode: 0o666, Rdev: 1<<32 | 3, ModTime: when},
			{Path: "home", Type: TypeDir, Mode: 0o755, UID: 1000, GID: 1000, ModTime: when},
		},
		content: map[string]string{
			"etc/motd":      "hola mundo\n",
			"usr/bin/sudo":  "SUDOBIN\n",
			"usr/bin/sudo2": "HARD\n",
			"usr/bin/sudo3": "HARD\n",
		},
	}
}

func TestPopulateAppliesEverything(t *testing.T) {
	src := testTree()
	tgt := newFakeTarget()

	stats, err := Populate(src, tgt, PopulateOptions{})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}

	if stats.Dirs != 5 || stats.Files != 3 || stats.Symlinks != 1 || stats.Devices != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.Hardlinks != 1 {
		t.Errorf("hardlinks = %d, se esperaba 1", stats.Hardlinks)
	}
	// 11 de motd + 8 de sudo + 5 de sudo2.
	if stats.Bytes != 24 {
		t.Errorf("bytes = %d, se esperaban 24", stats.Bytes)
	}
	if stats.Unsupported != 0 {
		t.Errorf("unsupported = %d", stats.Unsupported)
	}

	if got := tgt.content["etc/motd"]; got != "hola mundo\n" {
		t.Errorf("contenido de motd = %q", got)
	}
	if got := tgt.modes["usr/bin/sudo"]; got&fs.ModeSetuid == 0 {
		t.Errorf("se perdio el setuid: mode = %v", got)
	}
	if got := tgt.owners["home"]; got != [2]uint32{1000, 1000} {
		t.Errorf("dueno de home = %v", got)
	}
	if got := tgt.links["bin"]; got != "usr/bin" {
		t.Errorf("target del symlink = %q", got)
	}
	// El segundo camino del hardlink apunta al primero, y no se crea de nuevo.
	if got := tgt.links["usr/bin/sudo3"]; got != "usr/bin/sudo2" {
		t.Errorf("el hardlink apunta a %q", got)
	}
	if _, creado := tgt.content["usr/bin/sudo3"]; creado {
		t.Error("el hardlink no deberia haberse escrito otra vez")
	}
}

func TestPopulateOrderIsParentsThenContentThenTimes(t *testing.T) {
	tgt := newFakeTarget()
	if _, err := Populate(testTree(), tgt, PopulateOptions{}); err != nil {
		t.Fatalf("Populate: %v", err)
	}

	pos := func(op, path string) int {
		want := op + " " + path
		for i, o := range tgt.ops {
			if o == want {
				return i
			}
		}
		t.Fatalf("no se registro %q", want)
		return -1
	}

	// El padre antes que el hijo.
	if pos("mkdir", "etc") > pos("create", "etc/motd") {
		t.Error("el directorio deberia crearse antes que su contenido")
	}
	// El contenido antes que los permisos: si no, el setuid se pierde.
	if pos("close", "usr/bin/sudo") > pos("chmod", "usr/bin/sudo") {
		t.Error("los permisos deberian aplicarse despues de escribir")
	}

	// Todos los tiempos van despues de todos los contenidos.
	firstTimes := -1
	lastClose := -1
	for i, o := range tgt.ops {
		if strings.HasPrefix(o, "settimes ") && firstTimes < 0 {
			firstTimes = i
		}
		if strings.HasPrefix(o, "close ") {
			lastClose = i
		}
	}
	if firstTimes < 0 {
		t.Fatal("no se ajusto ningun tiempo")
	}
	if firstTimes < lastClose {
		t.Error("los tiempos deberian aplicarse tras el ultimo contenido")
	}

	// Y de hijo a padre: etc es el primero creado, asi que su tiempo va el ultimo.
	if pos("settimes", "etc") < pos("settimes", "usr") {
		t.Errorf("los tiempos deberian ir de hijo a padre: %v", tgt.ops[firstTimes:])
	}
}

func TestPopulateToleratesUnsupported(t *testing.T) {
	tgt := newFakeTarget()
	tgt.unsupported["mknod"] = true
	tgt.unsupported["hardlink"] = true

	// Sin tolerancia, el primer no soportado corta.
	if _, err := Populate(testTree(), tgt, PopulateOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, se esperaba ErrUnsupported", err)
	}

	// Con tolerancia, se cuenta y se sigue.
	tgt = newFakeTarget()
	tgt.unsupported["mknod"] = true
	tgt.unsupported["hardlink"] = true

	stats, err := Populate(testTree(), tgt, PopulateOptions{TolerateUnsupported: true})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if stats.Unsupported != 2 {
		t.Errorf("unsupported = %d, se esperaban 2", stats.Unsupported)
	}
	if stats.Hardlinks != 0 || stats.Devices != 0 {
		t.Errorf("no deberian contarse como aplicados: %+v", stats)
	}
	// El hardlink no soportado se copia: el fichero tiene que existir igual.
	if stats.Files != 4 || stats.Bytes != 29 {
		t.Errorf("el resto deberia copiarse igual: %+v", stats)
	}
	if _, ok := tgt.content["usr/bin/sudo3"]; !ok {
		t.Error("el segundo camino del hardlink deberia existir como copia")
	}
}

// Un buffer diminuto no puede alterar el contenido.
func TestPopulateWithTinyBuffer(t *testing.T) {
	src := &fakeSource{
		entries: []Entry{{Path: "grande.txt", Type: TypeRegular, Size: 37}},
		content: map[string]string{"grande.txt": strings.Repeat("a", 37)},
	}
	tgt := newFakeTarget()

	stats, err := Populate(src, tgt, PopulateOptions{Buffer: 4})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if got := tgt.content["grande.txt"]; got != strings.Repeat("a", 37) {
		t.Errorf("contenido de %d bytes", len(got))
	}
	if stats.Bytes != 37 {
		t.Errorf("bytes = %d", stats.Bytes)
	}
}

func TestPopulateStopsOnRealErrors(t *testing.T) {
	src := &fakeSource{
		entries: []Entry{{Path: "roto", Type: TypeRegular, Size: 4}},
		content: map[string]string{}, // el fichero no se puede abrir
	}
	tgt := newFakeTarget()

	_, err := Populate(src, tgt, PopulateOptions{TolerateUnsupported: true})
	if err == nil {
		t.Fatal("un fichero que no se puede abrir deberia abortar")
	}
	if !strings.Contains(err.Error(), "abrir") {
		t.Errorf("el error deberia citar el fichero: %v", err)
	}
}

func TestPopulateRejectsNil(t *testing.T) {
	if _, err := Populate(nil, newFakeTarget(), PopulateOptions{}); err == nil {
		t.Error("sin origen deberia fallar")
	}
	if _, err := Populate(testTree(), nil, PopulateOptions{}); err == nil {
		t.Error("sin destino deberia fallar")
	}
}
