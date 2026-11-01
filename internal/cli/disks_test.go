package cli

import (
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
)

func TestFindDiskFindsByNumber(t *testing.T) {
	disks := []blockdev.Disk{{Number: 0}, {Number: 2}, {Number: 3}}

	d, ok := findDisk(disks, 2)
	if !ok || d.Number != 2 {
		t.Fatalf("findDisk(2) = %v %v", d.Number, ok)
	}

	if _, ok := findDisk(disks, 7); ok {
		t.Fatal("un disco que no esta no puede encontrarse")
	}
	if _, ok := findDisk(nil, 0); ok {
		t.Fatal("sin discos no se encuentra nada")
	}
}

func TestDiskNumbersListsThemForTheErrorMessage(t *testing.T) {
	casos := []struct {
		discos []blockdev.Disk
		quiere string
	}{
		{nil, "ninguno"},
		{[]blockdev.Disk{{Number: 3}}, "3"},
		{[]blockdev.Disk{{Number: 0}, {Number: 1}, {Number: 3}}, "0, 1, 3"},
	}

	for _, c := range casos {
		if got := diskNumbers(c.discos); got != c.quiere {
			t.Errorf("diskNumbers(%v) = %q, queria %q", c.discos, got, c.quiere)
		}
	}
}

func TestEstadoIOCTL(t *testing.T) {
	if got := estadoIOCTL(nil); got != "ok" {
		t.Fatalf("nil = %q, queria ok", got)
	}

	got := estadoIOCTL(errTest{})
	if !strings.HasPrefix(got, "FALLO: ") || !strings.Contains(got, "prueba") {
		t.Fatalf("error = %q, queria FALLO con el texto", got)
	}
}

// errTest es un error minimo para no depender de errors.New en cada caso.
type errTest struct{}

func (errTest) Error() string { return "prueba" }
