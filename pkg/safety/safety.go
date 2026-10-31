package safety

import (
	"errors"
	"fmt"
	"strings"

	"distrogo/pkg/blockdev"
)

// Codigos de bloqueo. Un bloqueo no se puede saltar con ninguna flag.
const (
	ReasonSystem      = "system-disk"
	ReasonSystemMount = "protected-mount"
	ReasonReadOnly    = "read-only"
	ReasonNoSerial    = "no-serial"
	ReasonNoSize      = "no-size"
	ReasonWrongSerial = "serial-mismatch"
	ReasonSerialGone  = "serial-not-given"
)

// Codigos de aviso. Un aviso no impide escribir: informa de lo que se va a
// destruir o de lo que va a costar.
const (
	WarnMountedVolumes = "mounted-volumes"
	WarnExistingParts  = "existing-partitions"
	WarnNotUSB         = "not-usb"
)

// Reason explica un bloqueo o un aviso.
type Reason struct {
	Code   string
	Detail string
}

func (r Reason) String() string { return r.Detail }

// Verdict es el resultado de evaluar un disco.
//
// Eligible resume Reasons: si hay al menos un bloqueo, no se puede escribir.
// Warnings no bloquean nunca; describen lo que se va a perder.
// Destructive indica que escribir borra datos existentes.
type Verdict struct {
	DiskNumber  uint32
	Eligible    bool
	Destructive bool
	Reasons     []Reason
	Warnings    []Reason
}

// Options ajusta la evaluacion.
type Options struct {
	// AllowNonUSB silencia el aviso de bus no USB. Nunca bloquea: solo silencia.
	AllowNonUSB bool

	// ProtectedMountPoints son puntos de montaje que nunca se pueden tocar. El
	// llamante pasa aqui la unidad de %SystemRoot% como segunda red de
	// seguridad, independiente de IsSystem/IsBoot: si windisk se equivoca al
	// marcar el disco del sistema, esto lo sigue detectando.
	ProtectedMountPoints []string
}

// Assess evalua un disco.
//
// Solo bloquea por identidad (disco del sistema, dispositivo de solo lectura, sin
// serial o sin tamano). Tener volumenes montados o particiones no bloquea: se
// avisa de que se van a destruir. Esa es la diferencia entre proteger al usuario
// de un error y protegerlo de usar la herramienta.
func Assess(d blockdev.Disk, opts Options) Verdict {
	v := Verdict{DiskNumber: d.Number}

	deny := func(code, format string, args ...any) {
		v.Reasons = append(v.Reasons, Reason{Code: code, Detail: fmt.Sprintf(format, args...)})
	}
	warn := func(code, format string, args ...any) {
		v.Warnings = append(v.Warnings, Reason{Code: code, Detail: fmt.Sprintf(format, args...)})
	}

	if d.IsSystem || d.IsBoot {
		deny(ReasonSystem, "aloja el Windows en ejecucion: nunca es un objetivo valido")
	}

	if hits := matchMountPoints(d, opts.ProtectedMountPoints); len(hits) > 0 {
		deny(ReasonSystemMount, "aloja un volumen protegido: %s", strings.Join(hits, ", "))
	}

	if d.ReadOnly {
		deny(ReasonReadOnly, "el dispositivo esta en solo lectura")
	}

	if d.SizeBytes <= 0 {
		deny(ReasonNoSize, "no informa de tamano: Windows no devolvio la geometria")
	}

	if strings.TrimSpace(d.Serial) == "" {
		deny(ReasonNoSerial, "no informa de serial, asi que no se puede confirmar cual es")
	}

	if vols := mountedVolumeSummary(d); len(vols) > 0 {
		v.Destructive = true
		warn(WarnMountedVolumes,
			"tiene volumenes montados que se bloquearan y desmontaran; su contenido se pierde: %s",
			strings.Join(vols, ", "))
	}

	if len(d.Partitions) > 0 {
		v.Destructive = true
		warn(WarnExistingParts, "particiones existentes: %d; se sustituiran al escribir", len(d.Partitions))
	}

	if d.Bus != blockdev.BusUSB && !opts.AllowNonUSB {
		warn(WarnNotUSB, "el bus es %s y no usb; pasa --allow-non-usb si es intencionado", d.Bus)
	}

	v.Eligible = len(v.Reasons) == 0

	// Un disco bloqueado no se va a escribir, asi que hablar de lo que se
	// destruiria solo confunde. Los bloqueos son la unica informacion util.
	if !v.Eligible {
		v.Warnings = nil
		v.Destructive = false
	}

	return v
}

// AssessAll evalua todos los discos de un inventario.
func AssessAll(disks []blockdev.Disk, opts Options) []Verdict {
	out := make([]Verdict, 0, len(disks))
	for _, d := range disks {
		out = append(out, Assess(d, opts))
	}
	return out
}

// mountedVolumeSummary describe los volumenes montados con etiqueta, sistema de
// ficheros y tamano. Es lo que el usuario necesita ver para saber que va a
// perder si continua.
func mountedVolumeSummary(d blockdev.Disk) []string {
	var out []string
	for _, p := range d.Partitions {
		if p.Volume == nil || !p.Volume.Mounted() {
			continue
		}
		attrs := make([]string, 0, 3)
		if label := strings.TrimSpace(p.Volume.Label); label != "" {
			attrs = append(attrs, label)
		}
		if p.Volume.FileSystem != "" {
			attrs = append(attrs, p.Volume.FileSystem)
		}
		attrs = append(attrs, blockdev.HumanSize(p.SizeBytes))
		out = append(out, fmt.Sprintf("%s (%s)", p.Volume.MountPoint, strings.Join(attrs, ", ")))
	}
	return out
}

func matchMountPoints(d blockdev.Disk, wanted []string) []string {
	if len(wanted) == 0 {
		return nil
	}
	normalize := func(s string) string {
		return strings.ToUpper(strings.TrimRight(strings.TrimSpace(s), `\`))
	}
	set := make(map[string]struct{}, len(wanted))
	for _, w := range wanted {
		if n := normalize(w); n != "" {
			set[n] = struct{}{}
		}
	}
	var out []string
	for _, p := range d.Partitions {
		if p.Volume == nil {
			continue
		}
		if _, ok := set[normalize(p.Volume.MountPoint)]; ok {
			out = append(out, p.Volume.MountPoint)
		}
	}
	return out
}

// ErrRefused marca cualquier rechazo de escritura.
var ErrRefused = errors.New("escritura rechazada")

// Refusal detalla por que se rechaza un disco.
type Refusal struct {
	DiskNumber uint32
	Reasons    []Reason
}

func (r *Refusal) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "disco %d rechazado", r.DiskNumber)
	for _, reason := range r.Reasons {
		b.WriteString("; ")
		b.WriteString(reason.Detail)
	}
	return b.String()
}

func (r *Refusal) Unwrap() error { return ErrRefused }

// Request es la peticion de escritura del usuario.
//
// La confirmacion es implicita en las flags, al estilo diskpart: nombrar el disco
// (--target-disk) y su serial (--expect-serial). No hay preguntas interactivas.
type Request struct {
	// DiskNumber es el disco que el usuario cree estar eligiendo.
	DiskNumber uint32
	// ExpectSerial es el serial que el usuario leyo de `distrogo list`. Sin el no
	// se puede confirmar que el numero de disco siga apuntando al mismo
	// dispositivo: los numeros cambian al reconectar.
	ExpectSerial string
	// AllowNonUSB silencia el aviso de bus no USB.
	AllowNonUSB bool
	// ProtectedMountPoints, normalmente la unidad de %SystemRoot%.
	ProtectedMountPoints []string
}

// Authorize decide si se puede escribir en d. Devuelve nil si se puede y un
// *Refusal (que envuelve ErrRefused) si no.
//
// Destruir datos no es un motivo de rechazo: es un aviso que ya trae Assess. Lo
// que aqui se comprueba es la identidad del disco.
func Authorize(d blockdev.Disk, req Request) error {
	if d.Number != req.DiskNumber {
		return fmt.Errorf("el inventario devolvio el disco %d, no el disco %d que se pidio", d.Number, req.DiskNumber)
	}

	verdict := Assess(d, Options{
		AllowNonUSB:          req.AllowNonUSB,
		ProtectedMountPoints: req.ProtectedMountPoints,
	})
	reasons := verdict.Reasons

	expected := strings.TrimSpace(req.ExpectSerial)
	switch {
	case expected == "":
		reasons = append(reasons, Reason{
			Code:   ReasonSerialGone,
			Detail: "no se indico el serial esperado del disco (--expect-serial)",
		})
	case !strings.EqualFold(strings.TrimSpace(d.Serial), expected):
		reasons = append(reasons, Reason{
			Code:   ReasonWrongSerial,
			Detail: fmt.Sprintf("el disco informa del serial %q y se esperaba %q", d.Serial, req.ExpectSerial),
		})
	}

	if len(reasons) > 0 {
		return &Refusal{DiskNumber: d.Number, Reasons: reasons}
	}
	return nil
}
