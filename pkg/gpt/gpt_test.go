package gpt

import (
	"errors"
	"strings"
	"testing"
)

func TestTypeGUIDStrings(t *testing.T) {
	cases := []struct {
		guid GUID
		want string
	}{
		{TypeESP, "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"},
		{TypeLinuxFilesystem, "0fc63daf-8483-4772-8e79-3d69d8477de4"},
		{TypeLinuxSwap, "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f"},
		{TypeMicrosoftBasicData, "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7"},
	}
	for _, c := range cases {
		if got := c.guid.String(); got != c.want {
			t.Errorf("String() = %q, se esperaba %q", got, c.want)
		}
	}
}

func TestParseGUIDRoundTrip(t *testing.T) {
	for _, in := range []string{
		"c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
		"C12A7328-F81F-11D2-BA4B-00A0C93EC93B",
		"{c12a7328-f81f-11d2-ba4b-00a0c93ec93b}",
		"c12a7328f81f11d2ba4b00a0c93ec93b",
	} {
		g, err := ParseGUID(in)
		if err != nil {
			t.Fatalf("ParseGUID(%q): %v", in, err)
		}
		if g.String() != "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" {
			t.Errorf("ParseGUID(%q).String() = %q", in, g.String())
		}
	}
}

func TestParseGUIDRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "no-es-un-guid", "c12a7328-f81f-11d2-ba4b", strings.Repeat("zz", 16)} {
		if _, err := ParseGUID(in); !errors.Is(err, ErrBadGUID) {
			t.Errorf("ParseGUID(%q) deberia devolver ErrBadGUID, devolvio %v", in, err)
		}
	}
}

func TestNewGUIDIsVersion4AndUnique(t *testing.T) {
	seen := make(map[GUID]struct{}, 64)
	for i := 0; i < 64; i++ {
		g, err := NewGUID()
		if err != nil {
			t.Fatalf("NewGUID: %v", err)
		}
		if g.IsZero() {
			t.Fatal("NewGUID devolvio un GUID a cero")
		}
		if g[7]&0xf0 != 0x40 {
			t.Fatalf("version distinta de 4 en %s", g)
		}
		if g[8]&0xc0 != 0x80 {
			t.Fatalf("variante RFC 4122 incorrecta en %s", g)
		}
		if _, dup := seen[g]; dup {
			t.Fatalf("GUID repetido: %s", g)
		}
		seen[g] = struct{}{}
	}
}

func TestNewLayout8GiB(t *testing.T) {
	const size = 8 << 30
	l, err := NewLayout(size, 512)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}

	if l.TotalSectors != size/512 {
		t.Errorf("TotalSectors = %d", l.TotalSectors)
	}
	// 128 entradas de 128 bytes son 16 KiB, o sea 32 sectores de 512.
	if l.EntrySectors != 32 {
		t.Errorf("EntrySectors = %d, se esperaban 32", l.EntrySectors)
	}
	if l.FirstUsable != 34 {
		t.Errorf("FirstUsable = %d, se esperaban 34", l.FirstUsable)
	}
	if want := l.TotalSectors - 32 - 2; l.LastUsable != want {
		t.Errorf("LastUsable = %d, se esperaba %d", l.LastUsable, want)
	}
	if got := l.AlignSectors(); got != 2048 {
		t.Errorf("AlignSectors = %d, se esperaban 2048", got)
	}
}

func TestNewLayout4K(t *testing.T) {
	// Con sector de 4096 las 128 entradas ocupan 4 sectores.
	l, err := NewLayout(1<<30, 4096)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	if l.EntrySectors != 4 {
		t.Errorf("EntrySectors = %d, se esperaban 4", l.EntrySectors)
	}
	if l.FirstUsable != 6 {
		t.Errorf("FirstUsable = %d, se esperaban 6", l.FirstUsable)
	}
}

func TestNewLayoutRejectsInvalid(t *testing.T) {
	if _, err := NewLayout(8<<30, 300); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("sector de 300 bytes deberia fallar, deolvio %v", err)
	}
	if _, err := NewLayout(8<<30, 8192); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("sector de 8192 bytes deberia fallar, devolvio %v", err)
	}
	if _, err := NewLayout(0, 512); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("tamano 0 deberia fallar, devolvio %v", err)
	}
	// 32 KiB son 64 sectores: no caben la cabecera, las dos tablas y algo util.
	if _, err := NewLayout(32*1024, 512); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("un disco de 32 KiB no da para GPT, devolvio %v", err)
	}
}

func TestPlanESPAndRoot(t *testing.T) {
	const size = 8 << 30
	l, err := NewLayout(size, 512)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}

	specs := []SizeSpec{
		{Name: "ESP", Type: TypeESP, SizeBytes: 512 << 20},
		{Name: "root", Type: TypeLinuxFilesystem},
	}
	entries, err := Plan(l, specs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entradas = %d", len(entries))
	}

	esp, root := entries[0], entries[1]
	if esp.FirstLBA != 2048 {
		t.Errorf("la ESP empieza en %d, se esperaba 2048 (1 MiB)", esp.FirstLBA)
	}
	if esp.Sectors() != 2048*(512<<20)/DefaultAlignment {
		// 512 MiB / 512 bytes = 1048576 sectores, ya alineado.
		t.Errorf("la ESP ocupa %d sectores", esp.Sectors())
	}
	if esp.Sectors()*512 != 512<<20 {
		t.Errorf("la ESP ocupa %d bytes", esp.Sectors()*512)
	}
	if root.FirstLBA <= esp.LastLBA {
		t.Errorf("root empieza en %d y la ESP acaba en %d", root.FirstLBA, esp.LastLBA)
	}
	if root.FirstLBA%2048 != 0 {
		t.Errorf("root no esta alineada a 1 MiB: %d", root.FirstLBA)
	}
	if root.LastLBA != l.LastUsable {
		t.Errorf("root deberia acabar en %d, acaba en %d", l.LastUsable, root.LastLBA)
	}
	if esp.Type != TypeESP || root.Type != TypeLinuxFilesystem {
		t.Error("los tipos de particion no se conservaron")
	}
}

func TestPlanRejectsTooBig(t *testing.T) {
	l, err := NewLayout(1<<30, 512)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}

	_, err = Plan(l, []SizeSpec{
		{Name: "a", Type: TypeESP, SizeBytes: 2 << 30},
	})
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("error = %v, se esperaba ErrTooBig", err)
	}
}

func TestPlanRestOnlyOnLastSpec(t *testing.T) {
	l, _ := NewLayout(8<<30, 512)

	_, err := Plan(l, []SizeSpec{
		{Name: "a", Type: TypeESP},
		{Name: "b", Type: TypeLinuxFilesystem, SizeBytes: 1 << 20},
	})
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("error = %v, se esperaba ErrTooBig", err)
	}
}

func TestNewTableFillsGUIDs(t *testing.T) {
	l, _ := NewLayout(8<<30, 512)
	entries, err := Plan(l, []SizeSpec{
		{Name: "ESP", Type: TypeESP, SizeBytes: 512 << 20},
		{Name: "root", Type: TypeLinuxFilesystem},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	table, err := NewTable(GUID{}, entries)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	if table.DiskGUID.IsZero() {
		t.Error("el GUID del disco deberia rellenarse")
	}
	for i, e := range table.Partitions {
		if e.Unique.IsZero() {
			t.Errorf("la particion %d no tiene GUID unico", i+1)
		}
	}
	if err := table.Validate(l); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	other, err := NewTable(GUID{}, entries)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	if other.DiskGUID == table.DiskGUID {
		t.Error("dos tablas no deberian compartir GUID de disco")
	}
}

func TestValidateRejectsProblems(t *testing.T) {
	l, _ := NewLayout(8<<30, 512)
	disk, err := NewGUID()
	if err != nil {
		t.Fatalf("NewGUID: %v", err)
	}
	unique, err := NewGUID()
	if err != nil {
		t.Fatalf("NewGUID: %v", err)
	}

	base := func() *Table {
		return &Table{
			DiskGUID: disk,
			Partitions: []Entry{
				{Type: TypeESP, Unique: unique, FirstLBA: 2048, LastLBA: 4095, Name: "ESP"},
			},
		}
	}

	cases := map[string]struct {
		mutate func(*Table)
		want   error
	}{
		"sin guid de disco": {func(tb *Table) { tb.DiskGUID = GUID{} }, ErrInvalidLayout},
		"sin tipo":          {func(tb *Table) { tb.Partitions[0].Type = GUID{} }, ErrInvalidLayout},
		"sin guid unico":    {func(tb *Table) { tb.Partitions[0].Unique = GUID{} }, ErrInvalidLayout},
		"nombre largo":      {func(tb *Table) { tb.Partitions[0].Name = strings.Repeat("a", MaxNameUnits+1) }, ErrInvalidLayout},
		"fuera de rango":    {func(tb *Table) { tb.Partitions[0].LastLBA = l.LastUsable + 1 }, ErrOutOfRange},
		"antes del area":    {func(tb *Table) { tb.Partitions[0].FirstLBA = 1 }, ErrOutOfRange},
		"acaba antes":       {func(tb *Table) { tb.Partitions[0].LastLBA = tb.Partitions[0].FirstLBA - 1 }, ErrOutOfRange},
		"solape": {
			func(tb *Table) {
				tb.Partitions = append(tb.Partitions, Entry{
					Type: TypeLinuxFilesystem, Unique: unique,
					FirstLBA: tb.Partitions[0].LastLBA, LastLBA: tb.Partitions[0].LastLBA + 2048,
				})
			},
			ErrOverlap,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			table := base()
			c.mutate(table)
			if err := table.Validate(l); !errors.Is(err, c.want) {
				t.Fatalf("error = %v, se esperaba %v", err, c.want)
			}
		})
	}
}

func TestValidateAcceptsUnalignedOrder(t *testing.T) {
	l, _ := NewLayout(8<<30, 512)
	disk, _ := NewGUID()
	u1, _ := NewGUID()
	u2, _ := NewGUID()

	// Tabla con las particiones en orden inverso: el solape se comprueba tras
	// ordenar, no por posicion en el slice.
	table := &Table{
		DiskGUID: disk,
		Partitions: []Entry{
			{Type: TypeLinuxFilesystem, Unique: u2, FirstLBA: 4096, LastLBA: 8191, Name: "root"},
			{Type: TypeESP, Unique: u1, FirstLBA: 2048, LastLBA: 4095, Name: "ESP"},
		},
	}
	if err := table.Validate(l); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestEntrySectorsAndBytes(t *testing.T) {
	e := Entry{FirstLBA: 2048, LastLBA: 4095}
	if got := e.Sectors(); got != 2048 {
		t.Errorf("Sectors = %d", got)
	}
	if got := e.Bytes(512); got != 1<<20 {
		t.Errorf("Bytes = %d", got)
	}
}
