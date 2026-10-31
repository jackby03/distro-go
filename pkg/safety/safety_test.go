package safety

import (
	"errors"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
)

func mounted(mount, label, fs string) *blockdev.Volume {
	return &blockdev.Volume{MountPoint: mount, Label: label, FileSystem: fs}
}

func usbDisk(number uint32, serial string) blockdev.Disk {
	return blockdev.Disk{
		Number:         number,
		Model:          "USB3.0 externo",
		Serial:         serial,
		Bus:            blockdev.BusUSB,
		SizeBytes:      465 * 1 << 30,
		LogicalSector:  512,
		PhysicalSector: 4096,
	}
}

func TestAssessBlankUSBDiskIsEligible(t *testing.T) {
	v := Assess(usbDisk(3, "SN-PRUEBAS-0001"), Options{})

	if !v.Eligible {
		t.Fatalf("un disco USB vacio deberia ser elegible, bloqueos: %v", v.Reasons)
	}
	if v.Destructive {
		t.Fatal("un disco vacio no deberia marcarse como destructivo")
	}
	if len(v.Warnings) != 0 {
		t.Fatalf("avisos inesperados: %v", v.Warnings)
	}
}

func TestAssessRefusesSystemDisk(t *testing.T) {
	d := usbDisk(1, "ABC")
	d.IsBoot = true

	v := Assess(d, Options{AllowNonUSB: true})
	if v.Eligible {
		t.Fatal("el disco del sistema nunca deberia ser elegible")
	}
	if !hasReason(v, ReasonSystem) {
		t.Fatalf("se esperaba el bloqueo %q, hay %v", ReasonSystem, v.Reasons)
	}
}

// Segunda red de seguridad: aunque windisk no marque IsSystem, la unidad del
// sistema pasada explicitamente sigue bloqueando el disco.
func TestAssessRefusesProtectedMountPointEvenWithoutSystemFlag(t *testing.T) {
	d := usbDisk(4, "ABC")
	d.Partitions = []blockdev.Partition{{Number: 3, Volume: mounted(`C:\`, "", "NTFS")}}

	v := Assess(d, Options{AllowNonUSB: true, ProtectedMountPoints: []string{"c:"}})
	if v.Eligible {
		t.Fatal("un disco que aloja el punto de montaje protegido no deberia ser elegible")
	}
	if !hasReason(v, ReasonSystemMount) {
		t.Fatalf("se esperaba el bloqueo %q, hay %v", ReasonSystemMount, v.Reasons)
	}
}

// El cambio de politica de esta iteracion: un volumen montado ya no bloquea,
// avisa. Bloquearlo dejaba la herramienta inservible en el caso normal.
func TestAssessWarnsButAllowsMountedVolume(t *testing.T) {
	d := usbDisk(3, "ABC")
	d.Partitions = []blockdev.Partition{
		{Number: 1, SizeBytes: 500107862016, Volume: mounted(`E:\`, "New Volume", "exFAT")},
	}

	v := Assess(d, Options{})
	if !v.Eligible {
		t.Fatalf("un volumen montado no deberia bloquear, bloqueos: %v", v.Reasons)
	}
	if !v.Destructive {
		t.Fatal("con un volumen montado la escritura es destructiva")
	}
	if !hasWarning(v, WarnMountedVolumes) {
		t.Fatalf("se esperaba el aviso %q, hay %v", WarnMountedVolumes, v.Warnings)
	}

	// El aviso debe decir que se destruye, no solo que hay algo.
	detail := warningDetail(v, WarnMountedVolumes)
	for _, want := range []string{`E:\`, "New Volume", "exFAT", "465.8 GiB"} {
		if !strings.Contains(detail, want) {
			t.Errorf("el aviso deberia contener %q, dice %q", want, detail)
		}
	}
}

func TestAssessWarnsAboutExistingPartitions(t *testing.T) {
	d := usbDisk(3, "ABC")
	d.Partitions = []blockdev.Partition{{Number: 1, SizeBytes: 1 << 30}, {Number: 2, SizeBytes: 1 << 30}}

	v := Assess(d, Options{})
	if !v.Eligible {
		t.Fatalf("las particiones no deberian bloquear, bloqueos: %v", v.Reasons)
	}
	if !v.Destructive || !hasWarning(v, WarnExistingParts) {
		t.Fatalf("se esperaba aviso destructivo, hay %v", v.Warnings)
	}
}

func TestAssessNonUSBIsAWarningNotABlock(t *testing.T) {
	d := usbDisk(0, "ABC")
	d.Bus = blockdev.BusSATA

	v := Assess(d, Options{})
	if !v.Eligible {
		t.Fatalf("un disco SATA no deberia bloquear, bloqueos: %v", v.Reasons)
	}
	if !hasWarning(v, WarnNotUSB) {
		t.Fatalf("se esperaba el aviso %q, hay %v", WarnNotUSB, v.Warnings)
	}

	silenced := Assess(d, Options{AllowNonUSB: true})
	if hasWarning(silenced, WarnNotUSB) {
		t.Fatalf("--allow-non-usb deberia silenciar el aviso, hay %v", silenced.Warnings)
	}
}

func TestAssessRefusesReadOnlyNoSerialAndNoSize(t *testing.T) {
	ro := usbDisk(3, "ABC")
	ro.ReadOnly = true
	if v := Assess(ro, Options{}); !hasReason(v, ReasonReadOnly) {
		t.Fatalf("se esperaba el bloqueo %q, hay %v", ReasonReadOnly, v.Reasons)
	}

	noSerial := usbDisk(3, "   ")
	if v := Assess(noSerial, Options{}); !hasReason(v, ReasonNoSerial) {
		t.Fatalf("se esperaba el bloqueo %q, hay %v", ReasonNoSerial, v.Reasons)
	}

	noSize := usbDisk(3, "ABC")
	noSize.SizeBytes = 0
	if v := Assess(noSize, Options{}); !hasReason(v, ReasonNoSize) {
		t.Fatalf("se esperaba el bloqueo %q, hay %v", ReasonNoSize, v.Reasons)
	}
}

func TestAuthorizeHappyPath(t *testing.T) {
	d := usbDisk(3, "SN-PRUEBAS-0001")

	if err := Authorize(d, Request{DiskNumber: 3, ExpectSerial: "SN-PRUEBAS-0001"}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
}

func TestAuthorizeIsCaseInsensitiveOnSerial(t *testing.T) {
	d := usbDisk(3, "SN-PRUEBAS-0001")

	if err := Authorize(d, Request{DiskNumber: 3, ExpectSerial: "sn-pruebas-0001"}); err != nil {
		t.Fatalf("el serial deberia compararse sin distinguir mayusculas: %v", err)
	}
}

// Nombrar el disco y su serial es la confirmacion, al estilo diskpart: si el
// disco tiene datos, se escribe igual. Lo que no se permite es escribir sin
// haber confirmado la identidad.
func TestAuthorizeAllowsDestructiveWrite(t *testing.T) {
	d := usbDisk(2, "SN-DATOS-0001")
	d.Partitions = []blockdev.Partition{{Number: 1, Volume: mounted(`F:\`, "DATOS", "NTFS")}}

	if err := Authorize(d, Request{DiskNumber: 2, ExpectSerial: "SN-DATOS-0001"}); err != nil {
		t.Fatalf("un disco con datos deberia permitirse confirmando serial: %v", err)
	}
}

func TestAuthorizeRefusesWrongSerial(t *testing.T) {
	d := usbDisk(2, "SN-DATOS-0001")

	err := Authorize(d, Request{DiskNumber: 2, ExpectSerial: "SN-PRUEBAS-0001"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("error = %v, se esperaba ErrRefused", err)
	}
	if !hasReasonIn(refusalOf(t, err).Reasons, ReasonWrongSerial) {
		t.Fatalf("se esperaba el bloqueo %q, hay %v", ReasonWrongSerial, refusalOf(t, err).Reasons)
	}
}

func TestAuthorizeRequiresExpectedSerial(t *testing.T) {
	d := usbDisk(3, "SN-PRUEBAS-0001")

	err := Authorize(d, Request{DiskNumber: 3})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("error = %v, se esperaba ErrRefused", err)
	}
	if !hasReasonIn(refusalOf(t, err).Reasons, ReasonSerialGone) {
		t.Fatal("se esperaba el bloqueo de serial no indicado")
	}
}

func TestAuthorizeStillRefusesSystemDiskWithCorrectSerial(t *testing.T) {
	d := usbDisk(1, "SN-NVME-0001")
	d.IsSystem = true

	err := Authorize(d, Request{DiskNumber: 1, ExpectSerial: "SN-NVME-0001"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("error = %v, se esperaba ErrRefused", err)
	}
	if !hasReasonIn(refusalOf(t, err).Reasons, ReasonSystem) {
		t.Fatal("el serial correcto no puede desbloquear el disco del sistema")
	}
}

func TestAuthorizeRejectsMismatchedDiskNumber(t *testing.T) {
	d := usbDisk(3, "ABC")

	err := Authorize(d, Request{DiskNumber: 7, ExpectSerial: "ABC"})
	if err == nil {
		t.Fatal("se esperaba error")
	}
	if errors.Is(err, ErrRefused) {
		t.Fatal("un numero de disco incoherente es un fallo de programacion, no un rechazo de politica")
	}
	if !strings.Contains(err.Error(), "disco 3") || !strings.Contains(err.Error(), "disco 7") {
		t.Fatalf("el mensaje deberia citar ambos discos: %v", err)
	}
}

// Foto real de la maquina de desarrollo. Solo Disk 1 (Windows) esta bloqueado;
// el resto es elegible, aunque Disk 0 y Disk 2 sean destructivos.
func TestDevelopmentMachineOnlySystemDiskIsBlocked(t *testing.T) {
	disks := []blockdev.Disk{
		{
			Number: 0, Model: "SSD SATA 480GB", Serial: "SN-SATA-0001",
			Bus: blockdev.BusSATA, SizeBytes: 480103981056, LogicalSector: 512, PhysicalSector: 512,
			Partitions: []blockdev.Partition{{Number: 1, SizeBytes: 480101003264, Volume: mounted(`D:\`, "", "NTFS")}},
		},
		{
			Number: 1, Model: "NVMe 1TB", Serial: "SN-NVME-0001",
			Bus: blockdev.BusNVMe, SizeBytes: 1024209543168, LogicalSector: 512, PhysicalSector: 4096,
			IsBoot: true, IsSystem: true,
			Partitions: []blockdev.Partition{
				{Number: 1},
				{Number: 2},
				{Number: 3, SizeBytes: 1022304976896, Volume: mounted(`C:\`, "", "NTFS")},
				{Number: 4},
			},
		},
		{
			Number: 2, Model: "USB externo 1TB", Serial: "SN-DATOS-0001",
			Bus: blockdev.BusUSB, SizeBytes: 1000204883968, LogicalSector: 512, PhysicalSector: 4096,
			Partitions: []blockdev.Partition{{Number: 1, SizeBytes: 1000202043392, Volume: mounted(`F:\`, "DATOS", "NTFS")}},
		},
		{
			Number: 3, Model: "USB3.0 externo", Serial: "SN-PRUEBAS-0001",
			Bus: blockdev.BusUSB, SizeBytes: 500107862016, LogicalSector: 512, PhysicalSector: 4096,
			Partitions: []blockdev.Partition{{Number: 1, SizeBytes: 500105216000, Volume: mounted(`E:\`, "New Volume", "exFAT")}},
		},
	}

	verdicts := AssessAll(disks, Options{
		AllowNonUSB:          true,
		ProtectedMountPoints: []string{`C:\`},
	})

	for _, v := range verdicts {
		wantBlocked := v.DiskNumber == 1
		if v.Eligible == wantBlocked {
			t.Errorf("disco %d: eligible=%v, se esperaba bloqueado=%v (motivos: %v)",
				v.DiskNumber, v.Eligible, wantBlocked, v.Reasons)
		}
		if v.DiskNumber != 1 && !v.Destructive {
			t.Errorf("disco %d: tiene datos, deberia marcarse destructivo", v.DiskNumber)
		}
	}

	// Disk 3 acaba de formatearse a exFAT: debe seguir siendo elegible.
	for _, v := range verdicts {
		if v.DiskNumber == 3 && !v.Eligible {
			t.Fatalf("Disk 3 con exFAT deberia ser elegible, bloqueos: %v", v.Reasons)
		}
	}
}

func hasReason(v Verdict, code string) bool { return hasReasonIn(v.Reasons, code) }

func hasWarning(v Verdict, code string) bool {
	for _, r := range v.Warnings {
		if r.Code == code {
			return true
		}
	}
	return false
}

func warningDetail(v Verdict, code string) string {
	for _, r := range v.Warnings {
		if r.Code == code {
			return r.Detail
		}
	}
	return ""
}

func hasReasonIn(reasons []Reason, code string) bool {
	for _, r := range reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

func refusalOf(t *testing.T, err error) *Refusal {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("el error no es un *Refusal: %v", err)
	}
	return refusal
}
