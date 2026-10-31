package fat32

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestPlainShortAcceptsValidNames(t *testing.T) {
	cases := []struct {
		long string
		want string // 11 bytes, con espacios visibles
	}{
		{"BOOTX64.EFI", "BOOTX64 EFI"},
		{"EFI", "EFI        "},
		{"README.MD", "README  MD "},
		{"12345678.123", "12345678123"},
	}
	for _, c := range cases {
		base, ext := splitName(c.long)
		short, ok := plainShort(base, ext)
		if !ok {
			t.Errorf("%q deberia caber en 8.3", c.long)
			continue
		}
		if got := string(short[:]); got != c.want {
			t.Errorf("%q -> %q, se esperaba %q", c.long, got, c.want)
		}
	}
}

func TestPlainShortRejectsWhatNeedsLFN(t *testing.T) {
	// Minusculas, mas de 8 caracteres de base, mas de 3 de extension, espacios o
	// mas de un punto: todo eso necesita nombre largo.
	for _, name := range []string{
		"bootx64.efi", "x86_64-efi", "filesystem.squashfs", "Two Words.txt", "a.b.c",
	} {
		base, ext := splitName(name)
		if _, ok := plainShort(base, ext); ok {
			t.Errorf("%q no deberia caber en 8.3", name)
		}
	}
}

func TestPlanNameSimpleNameNeedsNoLFN(t *testing.T) {
	p, err := planName("BOOTX64.EFI", map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	if p.needsLFN() {
		t.Error("BOOTX64.EFI cabe en 8.3 y no deberia llevar nombre largo")
	}
	if got := string(p.short[:]); got != "BOOTX64 EFI" {
		t.Errorf("nombre corto = %q", got)
	}
}

func TestPlanNameLowercaseGetsLFN(t *testing.T) {
	p, err := planName("bootx64.efi", map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	if !p.needsLFN() {
		t.Fatal("un nombre en minusculas necesita LFN para conservar el caso")
	}
	if got := string(p.short[:]); got != "BOOTX64 EFI" {
		t.Errorf("nombre corto = %q", got)
	}
	if len(p.lfn) != 32 {
		t.Fatalf("entradas LFN = %d, se esperaba 1", len(p.lfn)/32)
	}

	e := p.lfn[0:32]
	if got := e[0]; got != 0x41 {
		t.Errorf("secuencia = 0x%02X, se esperaba 0x41 (unica y ultima)", got)
	}
	if got := e[11]; got != attrLFN {
		t.Errorf("atributo = 0x%02X", got)
	}
	if got := e[13]; got != lfnChecksum(p.short) {
		t.Errorf("checksum = 0x%02X", got)
	}
	if got := lfnText(e); got != "bootx64.efi" {
		t.Errorf("texto del LFN = %q", got)
	}
}

func TestPlanNameLongNameSplitsInto13CharChunks(t *testing.T) {
	const long = "x86_64-efi" // 10 caracteres: una entrada
	p, err := planName(long, map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	if got := len(p.lfn) / 32; got != 1 {
		t.Fatalf("entradas = %d, se esperaba 1", got)
	}

	// 26 caracteres: dos entradas, y la del disco va primero con 0x40.
	const long2 = "abcdefghijklmnopqrstuvwxyz"
	p2, err := planName(long2, map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	if got := len(p2.lfn) / 32; got != 2 {
		t.Fatalf("entradas = %d, se esperaban 2", got)
	}
	first := p2.lfn[0:32]
	second := p2.lfn[32:64]
	if got := first[0]; got != 0x42 {
		t.Errorf("primera entrada: secuencia = 0x%02X, se esperaba 0x42", got)
	}
	if got := second[0]; got != 0x01 {
		t.Errorf("segunda entrada: secuencia = 0x%02X, se esperaba 0x01", got)
	}
	// En el disco las entradas van al reves: la segunda tiene el primer trozo.
	if got := lfnText(second) + lfnText(first); got != long2 {
		t.Errorf("el nombre reconstruido es %q", got)
	}
}

func TestPlanNamePadsWithTerminatorAndFFFF(t *testing.T) {
	p, err := planName("a", map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	// "A" cabe en 8.3, asi que no hay LFN. Usamos una minuscula para forzarlo.
	p, err = planName("a.txt", map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	if len(p.lfn) != 32 {
		t.Fatalf("entradas = %d", len(p.lfn)/32)
	}

	e := p.lfn[0:32]
	if got := put32At(e, 1); got != 'a' {
		t.Errorf("primer caracter = %U", got)
	}
	if got := binary.LittleEndian.Uint16(e[lfnOffsets[5]:]); got != 0x0000 {
		t.Errorf("terminador = 0x%04X, se esperaba 0x0000", got)
	}
	if got := binary.LittleEndian.Uint16(e[lfnOffsets[6]:]); got != 0xFFFF {
		t.Errorf("relleno = 0x%04X, se esperaba 0xFFFF", got)
	}
}

func TestPlanNameSanitizesInvalidCharacters(t *testing.T) {
	p, err := planName("Two Words.txt", map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	if got := string(p.short[:]); got != "TWO_WO~1TXT" {
		t.Errorf("nombre corto = %q, se esperaba \"TWO_WO~1TXT\"", got)
	}
}

func TestPlanNameResolvesCollisions(t *testing.T) {
	taken := map[string]bool{}

	short1, err := planName("shared name", taken)
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	taken[string(short1.short[:])] = true

	short2, err := planName("shared name", taken)
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	taken[string(short2.short[:])] = true

	short3, err := planName("shared name", taken)
	if err != nil {
		t.Fatalf("planName: %v", err)
	}

	if short1.short == short2.short || short2.short == short3.short || short1.short == short3.short {
		t.Fatalf("nombres cortos repetidos: %q %q %q",
			short1.short, short2.short, short3.short)
	}
	if got := string(short2.short[:]); !strings.HasSuffix(strings.TrimRight(got, " "), "~2") {
		t.Errorf("el segundo deberia llevar ~2: %q", got)
	}
	if got := string(short3.short[:]); !strings.Contains(got, "~3") {
		t.Errorf("el tercero deberia llevar ~3: %q", got)
	}
}

func TestPlanNameRejectsGarbage(t *testing.T) {
	for _, name := range []string{"", ".", "..", strings.Repeat("a", 256)} {
		if _, err := planName(name, map[string]bool{}); !errors.Is(err, ErrBadName) {
			t.Errorf("planName(%q) = %v, se esperaba ErrBadName", name, err)
		}
	}
	// Un nombre que se queda sin letras utiles no da base.
	if _, err := planName("...", map[string]bool{}); !errors.Is(err, ErrBadName) {
		t.Error("un nombre sin base util deberia fallar")
	}
}

func TestLFNChecksumIsStableAndDistinct(t *testing.T) {
	a, _ := planName("uno.txt", map[string]bool{})
	b, _ := planName("dos.txt", map[string]bool{})
	c, _ := planName("uno.txt", map[string]bool{})

	if lfnChecksum(a.short) != lfnChecksum(c.short) {
		t.Error("el mismo nombre deberia dar el mismo checksum")
	}
	if lfnChecksum(a.short) == lfnChecksum(b.short) {
		t.Error("dos nombres distintos no deberian compartir checksum")
	}

	// Todas las entradas de un mismo nombre llevan el mismo checksum.
	long, err := planName("abcdefghijklmnopqrstuvwxyz.txt", map[string]bool{})
	if err != nil {
		t.Fatalf("planName: %v", err)
	}
	want := lfnChecksum(long.short)
	for i := 0; i < len(long.lfn)/32; i++ {
		if got := long.lfn[i*32+13]; got != want {
			t.Errorf("entrada %d: checksum 0x%02X, se esperaba 0x%02X", i, got, want)
		}
	}
}

func TestLFNEntryCount(t *testing.T) {
	cases := map[string]int{
		"bootx64.efi":                         1, // 11 caracteres
		"abcdefghijklm":                       1, // 13 exactos, sin terminador
		"abcdefghijklmn":                      2, // 14
		"abcdefghijklmnopqrstuvwxyz":          2, // 26
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": 3, // 35
	}
	for name, want := range cases {
		p, err := planName(name, map[string]bool{})
		if err != nil {
			t.Fatalf("planName(%q): %v", name, err)
		}
		if got := len(p.lfn) / 32; got != want {
			t.Errorf("%q ocupa %d entradas LFN, se esperaban %d", name, got, want)
		}
	}
}

// lfnText reconstruye el nombre de una entrada LFN.
func lfnText(e []byte) string {
	units := make([]uint16, 0, 13)
	for _, off := range lfnOffsets {
		units = append(units, binary.LittleEndian.Uint16(e[off:off+2]))
	}
	// Corta en el terminador.
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
		if u == 0xFFFF {
			units = units[:i]
			break
		}
	}
	return string(utf16.Decode(units))
}

func put32At(e []byte, off int) rune {
	return rune(binary.LittleEndian.Uint16(e[off : off+2]))
}
