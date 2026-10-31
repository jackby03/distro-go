package fat32

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

const (
	// maxLongName es el limite de un nombre largo, en unidades UTF-16.
	maxLongName = 255

	// attrVolumeID y attrDirectory son los atributos que nos importan.
	attrVolumeID  = 0x08
	attrDirectory = 0x10
	attrLFN       = 0x0F
)

// ErrBadName indica un nombre que no se puede representar.
var ErrBadName = errors.New("nombre de fichero invalido")

// allowedShort dice que caracteres admite un nombre 8.3 ademas de letras y
// digitos. Es la lista de la especificacion de FAT.
const allowedShort = "$%'-_@~`!(){}^#&"

// lfnOffsets son las posiciones, dentro de la entrada de 32 bytes, donde van los
// 13 caracteres UTF-16 del nombre largo.
var lfnOffsets = [13]int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}

// namePlan es lo que hay que escribir en el directorio para un nombre.
type namePlan struct {
	// short son los 11 bytes del nombre 8.3, ya rellenados con espacios.
	short [11]byte
	// lfn son las entradas de nombre largo, serializadas y en el orden en que
	// van al disco (la ultima primero). Vacio si el nombre cabe en 8.3.
	lfn []byte
}

// needsLFN indica si el nombre largo tiene que escribirse aparte.
func (p namePlan) needsLFN() bool { return len(p.lfn) > 0 }

// planName decide el nombre 8.3 y, si hace falta, las entradas LFN.
//
// taken son los nombres 8.3 ya usados en el directorio, sin rellenar. Un nombre
// que ya cabe en 8.3 y esta libre se usa tal cual; el resto recibe una tilde y un
// numero, y el nombre original viaja en las entradas LFN.
func planName(long string, taken map[string]bool) (namePlan, error) {
	if long == "" || long == "." || long == ".." {
		return namePlan{}, fmt.Errorf("%w: %q", ErrBadName, long)
	}
	if units := len(utf16.Encode([]rune(long))); units > maxLongName {
		return namePlan{}, fmt.Errorf("%w: %q ocupa %d unidades UTF-16, maximo %d",
			ErrBadName, long, units, maxLongName)
	}
	if strings.Trim(long, ". ") == "" {
		return namePlan{}, fmt.Errorf("%w: %q no tiene nombre util", ErrBadName, long)
	}

	base, ext := splitName(long)

	// Camino corto: si ya es un 8.3 valido en mayusculas y esta libre, no hay
	// nada que inventar.
	if short, ok := plainShort(base, ext); ok && !taken[string(short[:])] {
		return namePlan{short: short}, nil
	}

	// Si solo falla el caso, se usa el nombre en mayusculas y el original viaja
	// en el LFN. Es lo que hace Windows: BOOTX64.EFI con nombre largo
	// "bootx64.efi", y no BOOTX6~1.EFI.
	if short, ok := plainShort(asciiUpper(base), asciiUpper(ext)); ok && !taken[string(short[:])] {
		return namePlan{short: short, lfn: buildLFN(long, short)}, nil
	}

	cleanBase := sanitize(base, 8)
	cleanExt := sanitize(ext, 3)
	if cleanBase == "" {
		return namePlan{}, fmt.Errorf("%w: %q no deja nada para el nombre corto", ErrBadName, long)
	}

	// Con numero: la base se recorta para que quepa la tilde y el sufijo.
	for n := 1; n <= 999999; n++ {
		suffix := fmt.Sprintf("~%d", n)
		room := 8 - len(suffix)
		if room <= 0 {
			return namePlan{}, fmt.Errorf("%w: %q no cabe en 8.3", ErrBadName, long)
		}
		stem := cleanBase
		if len(stem) > room {
			stem = stem[:room]
		}
		if stem == "" {
			return namePlan{}, fmt.Errorf("%w: %q no deja base para el nombre corto", ErrBadName, long)
		}

		var short [11]byte
		copy(short[0:8], leftPad(stem+suffix, 8))
		copy(short[8:11], leftPad(cleanExt, 3))
		if taken[string(short[:])] {
			continue
		}
		return namePlan{short: short, lfn: buildLFN(long, short)}, nil
	}

	return namePlan{}, fmt.Errorf("%w: %q se queda sin sufijos libres", ErrBadName, long)
}

// asciiUpper pasa a mayusculas solo las letras ASCII. No toca el resto para no
// cambiar la longitud de los nombres con bytes altos.
func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// splitName separa base y extension por el ultimo punto, si no esta al principio.
func splitName(name string) (base, ext string) {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// plainShort devuelve el nombre 8.3 si ya lo es y no hay que tocar nada.
func plainShort(base, ext string) ([11]byte, bool) {
	var short [11]byte
	if len(base) == 0 || len(base) > 8 || len(ext) > 3 {
		return short, false
	}
	for _, s := range []string{base, ext} {
		for i := 0; i < len(s); i++ {
			if !isShortChar(s[i]) {
				return short, false
			}
		}
	}
	copy(short[0:8], leftPad(base, 8))
	copy(short[8:11], leftPad(ext, 3))
	return short, true
}

// sanitize deja un nombre presentable para 8.3: mayusculas, sin caracteres
// prohibidos y recortado.
func sanitize(s string, max int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c >= 0x80:
			b.WriteByte(c)
		case strings.IndexByte(allowedShort, c) >= 0:
			b.WriteByte(c)
		case c == '.' || c == ' ':
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// isShortChar acepta lo que admite un nombre 8.3 sin conversion: letras,
// digitos y los simbolos permitidos, siempre en mayusculas.
func isShortChar(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c >= 0x80:
		return true
	case strings.IndexByte(allowedShort, c) >= 0:
		return true
	default:
		return false
	}
}

// lfnChecksum es el checksum del nombre corto que se copia en cada entrada LFN.
// Es el algoritmo de la especificacion: una rotacion a la derecha de un bit con
// acarreo, sumando cada byte.
func lfnChecksum(short [11]byte) byte {
	var sum byte
	for _, c := range short {
		sum = (sum >> 1) | (sum << 7)
		sum += c
	}
	return sum
}

// buildLFN serializa las entradas de nombre largo.
//
// Van en orden inverso: la primera entrada del disco es la ultima del nombre y
// lleva el bit 0x40. Cada entrada guarda 13 unidades UTF-16 repartidas en
// posiciones fijas.
func buildLFN(long string, short [11]byte) []byte {
	units := utf16.Encode([]rune(long))
	chunks := (len(units) + 12) / 13
	if chunks == 0 {
		return nil
	}

	out := make([]byte, chunks*32)
	sum := lfnChecksum(short)

	for i := 0; i < chunks; i++ {
		// La entrada i-esima del nombre ocupa la posicion chunks-1-i en el disco.
		e := out[(chunks-1-i)*32:]
		seq := byte(i + 1)
		if i == chunks-1 {
			seq |= 0x40
		}
		e[0] = seq
		e[11] = attrLFN
		e[12] = 0
		e[13] = sum
		e[26], e[27] = 0, 0

		for j := 0; j < 13; j++ {
			idx := i*13 + j
			var v uint16
			switch {
			case idx < len(units):
				v = units[idx]
			case idx == len(units):
				v = 0x0000 // terminador
			default:
				v = 0xFFFF // relleno
			}
			put16(e, lfnOffsets[j], v)
		}
	}
	return out
}
