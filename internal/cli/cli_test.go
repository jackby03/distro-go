package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/windisk"
)

func volume(mount, label, fs string) *blockdev.Volume {
	return &blockdev.Volume{MountPoint: mount, Label: label, FileSystem: fs}
}

// fakeInventory reproduce la maquina de desarrollo: un disco SATA con datos, el
// disco de Windows, y dos USB, uno de ellos con datos del usuario.
func fakeInventory() windisk.Inventory {
	return windisk.Inventory{Disks: []blockdev.Disk{
		{
			Number: 0, Model: "KINGSTON SA400S37480G", Serial: "50026B77837DB62E",
			Bus: blockdev.BusSATA, SizeBytes: 480103981056, LogicalSector: 512, PhysicalSector: 512,
			Partitions: []blockdev.Partition{
				{Number: 1, SizeBytes: 480101003264, Volume: volume(`D:\`, "", "NTFS")},
			},
		},
		{
			Number: 1, Model: "Patriot M.2 P300 1024GB", Serial: "P300DBBA23072702055 _00000001.",
			Bus: blockdev.BusNVMe, SizeBytes: 1024209543168, LogicalSector: 512, PhysicalSector: 4096,
			IsBoot: true, IsSystem: true,
			Partitions: []blockdev.Partition{
				{Number: 1, SizeBytes: 104857600, TypeGUID: "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"},
				{Number: 2, SizeBytes: 16777216, TypeGUID: "e3c9e316-0b5c-4db8-817d-f92df00215ae"},
				{Number: 3, SizeBytes: 1022304976896, TypeGUID: "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7", Volume: volume(`C:\`, "", "NTFS")},
			},
		},
		{
			Number: 2, Model: "TOSHIBA External USB 3.0", Serial: "20171202002528F",
			Bus: blockdev.BusUSB, SizeBytes: 1000204883968, LogicalSector: 512, PhysicalSector: 4096,
			Partitions: []blockdev.Partition{
				{Number: 1, SizeBytes: 1000202043392, Volume: volume(`F:\`, "DD  JACK", "NTFS")},
			},
		},
		{
			Number: 3, Model: "External USB3.0", Serial: "201703310007F",
			Bus: blockdev.BusUSB, SizeBytes: 500107862016, LogicalSector: 512, PhysicalSector: 4096,
			Partitions: []blockdev.Partition{
				{Number: 1, SizeBytes: 500105216000, TypeGUID: "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7", Volume: volume(`E:\`, "New Volume", "exFAT")},
			},
		},
	}}
}

func newTestApp(inv windisk.Inventory) (*App, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	return &App{
		Out: out,
		Err: errBuf,
		Enumerate: func() (windisk.Inventory, error) {
			return inv, nil
		},
		ProtectedMounts: []string{`C:\`},
	}, out, errBuf
}

func TestListHumanShowsStates(t *testing.T) {
	app, out, _ := newTestApp(fakeInventory())

	if code := app.Run([]string{"list"}); code != exitOK {
		t.Fatalf("codigo de salida = %d", code)
	}

	text := out.String()
	for _, want := range []string{
		"4 discos",
		"Disco 0", "Disco 1", "Disco 2", "Disco 3",
		"BLOQUEADO",
		"ELEGIBLE (destructivo)",
		"aloja el Windows en ejecucion",
		"New Volume",
		"exFAT",
		"c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("la salida deberia contener %q", want)
		}
	}
}

// Un disco bloqueado no debe mostrar avisos de destruccion: no se va a escribir.
func TestListHumanDoesNotWarnAboutBlockedDisk(t *testing.T) {
	app, out, _ := newTestApp(fakeInventory())

	if code := app.Run([]string{"list"}); code != exitOK {
		t.Fatalf("codigo de salida = %d", code)
	}

	for _, block := range strings.Split(out.String(), "\n\n") {
		if !strings.Contains(block, "Disco 1") {
			continue
		}
		if strings.Contains(block, "aviso") {
			t.Errorf("el disco bloqueado no deberia mostrar avisos:\n%s", block)
		}
		return
	}
	t.Fatal("no se encontro el bloque del disco 1")
}

func TestListJSON(t *testing.T) {
	app, out, errBuf := newTestApp(fakeInventory())

	if code := app.Run([]string{"list", "--json"}); code != exitOK {
		t.Fatalf("codigo de salida = %d, stderr: %s", code, errBuf.String())
	}

	var report jsonReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("JSON invalido: %v\n%s", err, out.String())
	}
	if len(report.Disks) != 4 {
		t.Fatalf("discos en el JSON = %d", len(report.Disks))
	}

	blocked := report.Disks[1]
	if blocked.Eligible {
		t.Error("el disco 1 no deberia ser elegible")
	}
	if len(blocked.Reasons) == 0 {
		t.Error("el disco bloqueado deberia traer motivos")
	}
	if blocked.Destructive {
		t.Error("un disco bloqueado no deberia marcarse destructivo")
	}

	target := report.Disks[3]
	if !target.Eligible || !target.Destructive {
		t.Errorf("el disco 3 deberia ser elegible y destructivo: %+v", target)
	}
	if target.Serial != "201703310007F" {
		t.Errorf("serial = %q", target.Serial)
	}
	if len(target.Partitions) != 1 || target.Partitions[0].FileSystem != "exFAT" {
		t.Errorf("particiones = %+v", target.Partitions)
	}

	if strings.Contains(out.String(), "Disco 0") {
		t.Error("--json no deberia mezclar salida humana")
	}
}

func TestListAllowsNonUSBFlag(t *testing.T) {
	app, out, _ := newTestApp(fakeInventory())

	if code := app.Run([]string{"list", "--allow-non-usb"}); code != exitOK {
		t.Fatalf("codigo de salida = %d", code)
	}
	if strings.Contains(out.String(), "el bus es sata") {
		t.Error("--allow-non-usb deberia silenciar el aviso de bus")
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"sin argumentos", nil, exitUsage},
		{"comando desconocido", []string{"instal"}, exitUsage},
		{"flag desconocida", []string{"list", "--nope"}, exitUsage},
		{"argumentos de mas", []string{"list", "3"}, exitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app, _, _ := newTestApp(fakeInventory())
			if got := app.Run(c.args); got != c.want {
				t.Fatalf("codigo = %d, se esperaba %d", got, c.want)
			}
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	app, out, _ := newTestApp(fakeInventory())

	if code := app.Run([]string{"help"}); code != exitOK {
		t.Fatalf("codigo = %d", code)
	}
	if !strings.Contains(out.String(), "distrogo list") {
		t.Error("la ayuda deberia mencionar el comando list")
	}
}

func TestEnumerateErrorExitsOne(t *testing.T) {
	app := &App{
		Out: &bytes.Buffer{},
		Err: &bytes.Buffer{},
		Enumerate: func() (windisk.Inventory, error) {
			return windisk.Inventory{}, errors.New("el dispositivo no existe")
		},
	}

	if code := app.Run([]string{"list"}); code != exitError {
		t.Fatalf("codigo = %d, se esperaba %d", code, exitError)
	}
	if !strings.Contains(app.Err.(*bytes.Buffer).String(), "el dispositivo no existe") {
		t.Error("el error deberia llegar a stderr")
	}
}

func TestMissingEnumeratorIsAnError(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	if code := app.Run([]string{"list"}); code != exitError {
		t.Fatalf("codigo = %d, se esperaba %d", code, exitError)
	}
}

func TestSystemMountsUsesSystemRoot(t *testing.T) {
	t.Setenv("SystemRoot", `X:\Windows`)

	mounts := SystemMounts()
	if len(mounts) != 1 || mounts[0] != `X:\` {
		t.Fatalf("SystemMounts() = %v", mounts)
	}
}

func TestSystemMountsWithoutSystemRoot(t *testing.T) {
	t.Setenv("SystemRoot", "")

	if mounts := SystemMounts(); mounts != nil {
		t.Fatalf("SystemMounts() = %v, se esperaba nil", mounts)
	}
}
