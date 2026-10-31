package bootcfg

import (
	"slices"
	"strings"
	"testing"
)

// fstabLines devuelve las entradas como campos, sin depender del ancho de las
// columnas.
func fstabLines(t *testing.T, text string) [][]string {
	t.Helper()
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.Fields(line))
	}
	return out
}

func TestFstab(t *testing.T) {
	out, err := Fstab([]Mount{
		{Spec: UUIDSpec("b8ec581b-e706-4527-9acd-520ecd6f132d"), Dir: "/", Type: "ext4", Options: "errors=remount-ro", Pass: 1},
		{Spec: UUIDSpec("06B1-171A"), Dir: "/boot/efi", Type: "vfat", Options: "umask=0077", Pass: 1},
	})
	if err != nil {
		t.Fatalf("Fstab: %v", err)
	}
	if !strings.Contains(out, "generado por DistroGo") {
		t.Errorf("falta la cabecera:\n%s", out)
	}

	lines := fstabLines(t, out)
	want := [][]string{
		{"UUID=b8ec581b-e706-4527-9acd-520ecd6f132d", "/", "ext4", "errors=remount-ro", "0", "1"},
		{"UUID=06B1-171A", "/boot/efi", "vfat", "umask=0077", "0", "1"},
	}
	if len(lines) != len(want) {
		t.Fatalf("lineas = %d, se esperaban %d:\n%s", len(lines), len(want), out)
	}
	for i := range want {
		if !slices.Equal(lines[i], want[i]) {
			t.Errorf("linea %d = %v, se esperaba %v", i+1, lines[i], want[i])
		}
	}
}

func TestFstabDefaults(t *testing.T) {
	out, err := Fstab([]Mount{{Spec: UUIDSpec("x"), Dir: "/", Type: "ext4"}})
	if err != nil {
		t.Fatalf("Fstab: %v", err)
	}
	if got := fstabLines(t, out)[0][3]; got != "defaults" {
		t.Errorf("opciones = %q, se esperaba defaults", got)
	}
}

func TestFstabRejectsBadInput(t *testing.T) {
	cases := map[string][]Mount{
		"sin entradas":         nil,
		"primera no es raiz":   {{Spec: "UUID=x", Dir: "/boot", Type: "ext4"}},
		"opciones con espacio": {{Spec: "UUID=x", Dir: "/", Type: "ext4", Options: "a b"}},
		"sin spec":             {{Dir: "/", Type: "ext4"}},
		"sin tipo":             {{Spec: "UUID=x", Dir: "/"}},
	}
	for name, mounts := range cases {
		if _, err := Fstab(mounts); err == nil {
			t.Errorf("%s: deberia fallar", name)
		}
	}
}

func TestSpecHelpers(t *testing.T) {
	if got := UUIDSpec(" abc "); got != "UUID=abc" {
		t.Errorf("UUIDSpec = %q", got)
	}
	if got := PARTUUIDSpec("0fc63daf"); got != "PARTUUID=0fc63daf" {
		t.Errorf("PARTUUIDSpec = %q", got)
	}
}
