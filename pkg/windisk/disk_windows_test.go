//go:build windows

package windisk

import (
	"errors"
	"testing"

	"distrogo/pkg/blockdev"
)

// discoDePrueba devuelve un Disk sin handle: solo sirve para las funciones que
// calculan alineacion, que no tocan el hardware.
func discoDePrueba(size int64, sector int) *Disk {
	return &Disk{size: size, sector: sector}
}

func TestAlineado(t *testing.T) {
	d := discoDePrueba(100<<20, 512)

	casos := []struct {
		off    int64
		n      int
		quiere bool
	}{
		{0, 512, true},
		{512, 4096, true},
		{1024, 512, true},
		// El GDT del ext4 del primer intento real: 238.272 bytes, no multiplo de 512.
		{537923584, 238272, false},
		// La semilla de checksums: 4 bytes en un offset no alineado.
		{1024 + 0x270, 4, false},
		{1536, 4, false},
		{1536, 512, true},
	}

	for _, c := range casos {
		if got := d.alineado(c.off, c.n); got != c.quiere {
			t.Errorf("alineado(%d, %d) = %v, queria %v", c.off, c.n, got, c.quiere)
		}
	}
}

func TestRedondearSubeAlSiguienteSector(t *testing.T) {
	d := discoDePrueba(1000*512, 512)

	casos := map[int64]int64{
		0:      0,
		1:      512,
		512:    512,
		513:    1024,
		238272: 238592,
	}

	for entrada, quiere := range casos {
		if got := d.redondear(entrada); got != quiere {
			t.Errorf("redondear(%d) = %d, queria %d", entrada, got, quiere)
		}
	}
}

func TestRedondearNoSePasaDelDisco(t *testing.T) {
	d := discoDePrueba(2*512, 512)

	// Pedir el redondeo del ultimo byte no puede devolver un offset fuera del disco.
	if got := d.redondear(2*512 - 1); got != 2*512 {
		t.Fatalf("redondear = %d, queria %d", got, 2*512)
	}
}

func TestAccesoFueraDeRango(t *testing.T) {
	d := discoDePrueba(1024, 512)

	// El rango se comprueba antes de tocar el handle, asi que estos dos errores
	// son de rango y no de handle invalido.
	if _, err := d.ReadAt(make([]byte, 512), 1024); !errors.Is(err, blockdev.ErrOutOfRange) {
		t.Fatalf("leer pasado el final = %v, queria ErrOutOfRange", err)
	}
	if _, err := d.WriteAt(make([]byte, 512), 1024); !errors.Is(err, blockdev.ErrOutOfRange) {
		t.Fatalf("escribir pasado el final = %v, queria ErrOutOfRange", err)
	}
	if _, err := d.ReadAt(make([]byte, 1024), -1); !errors.Is(err, blockdev.ErrOutOfRange) {
		t.Fatalf("leer con offset negativo = %v, queria ErrOutOfRange", err)
	}
}

func TestDiscoCerradoNoDejaLeerNiEscribir(t *testing.T) {
	// Sin abrir: handle invalido.
	d := discoDePrueba(1024, 512)

	if _, err := d.ReadAt(make([]byte, 512), 0); err == nil {
		t.Fatal("un disco sin abrir no puede leerse")
	}
	if _, err := d.WriteAt(make([]byte, 512), 0); err == nil {
		t.Fatal("un disco sin abrir no puede escribirse")
	}
}
