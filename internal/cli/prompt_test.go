package cli

import (
	"errors"
	"strings"
	"testing"
)

// teclea pasa una cadena por el editor, caracter a caracter, y devuelve lo que
// quedo escrito y lo que se imprimio en pantalla.
func teclea(t *testing.T, entrada string) (string, string, error) {
	t.Helper()

	var pantalla strings.Builder
	editor := newPasswordEditor(&pantalla)

	for _, r := range entrada {
		done, err := editor.feed(r)
		if err != nil {
			return editor.text(), pantalla.String(), err
		}
		if done {
			break
		}
	}
	return editor.text(), pantalla.String(), nil
}

func TestPasswordEditorMaskEachKey(t *testing.T) {
	texto, pantalla, err := teclea(t, "hola\r")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "hola" {
		t.Fatalf("texto = %q, queria %q", texto, "hola")
	}
	// Un asterisco por tecla, mas el salto de linea final.
	if pantalla != "****\n" {
		t.Fatalf("pantalla = %q, queria %q", pantalla, "****\n")
	}
}

func TestPasswordEditorNeverEchoesTheCharacters(t *testing.T) {
	secreto := "Tr0ub4dor&3"
	_, pantalla, err := teclea(t, secreto+"\r")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if strings.Contains(pantalla, "T") || strings.Contains(pantalla, "r") {
		t.Fatalf("la pantalla no puede contener la contrasena: %q", pantalla)
	}
	if strings.Count(pantalla, string(passwordMask)) != len([]rune(secreto)) {
		t.Fatalf("asteriscos = %d, queria %d", strings.Count(pantalla, string(passwordMask)), len([]rune(secreto)))
	}
}

func TestPasswordEditorBackspaceErasesFromScreen(t *testing.T) {
	// "abc" y dos borrados dejan "a".
	texto, pantalla, err := teclea(t, "abc\b\b\r")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "a" {
		t.Fatalf("texto = %q, queria %q", texto, "a")
	}
	// Cada borrado tiene que retroceder, tapar con espacio y volver.
	if strings.Count(pantalla, "\b \b") != 2 {
		t.Fatalf("la pantalla no borro los dos asteriscos: %q", pantalla)
	}
}

func TestPasswordEditorBackspaceOnEmptyIsHarmless(t *testing.T) {
	texto, pantalla, err := teclea(t, "\b\b\bx\r")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "x" {
		t.Fatalf("texto = %q, queria %q", texto, "x")
	}
	if strings.Count(pantalla, "\b \b") != 0 {
		t.Fatalf("no deberia borrar nada si no hay nada: %q", pantalla)
	}
}

func TestPasswordEditorIgnoresControlKeys(t *testing.T) {
	// Tab, una flecha en ANSI (ESC [ A) y otra con prefijo de consola (0x00 +
	// codigo) no son parte de la contrasena, y su codigo de tecla tampoco.
	texto, pantalla, err := teclea(t, "\tab\x1b[A\x00Hcd\r")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "abcd" {
		t.Fatalf("texto = %q, queria %q", texto, "abcd")
	}
	if strings.Count(pantalla, string(passwordMask)) != 4 {
		t.Fatalf("asteriscos = %q, queria 4", pantalla)
	}
}

func TestPasswordEditorIgnoresHomeAndEndKeys(t *testing.T) {
	// Inicio y Fin llegan como ESC [ H y ESC [ F en ANSI, y como ESC O H en
	// modo aplicacion. Ninguna de las tres formas puede dejar una H o una F.
	texto, _, err := teclea(t, "ab\x1b[H\x1b[F\x1bOHcd\r")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "abcd" {
		t.Fatalf("texto = %q, queria %q", texto, "abcd")
	}
}

func TestPasswordEditorCtrlCCancels(t *testing.T) {
	_, _, err := teclea(t, "abc\x03")
	if !errors.Is(err, ErrPromptInterrupted) {
		t.Fatalf("err = %v, queria ErrPromptInterrupted", err)
	}
}

func TestReadPasswordFromReadsUntilEndOfLine(t *testing.T) {
	var pantalla strings.Builder
	texto, err := readPasswordFrom(strings.NewReader("clave\nresto"), &pantalla)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "clave" {
		t.Fatalf("texto = %q, queria %q", texto, "clave")
	}
	if pantalla.String() != "*****\n" {
		t.Fatalf("pantalla = %q", pantalla.String())
	}
}

func TestReadPasswordFromAcceptsEOFWithoutNewline(t *testing.T) {
	var pantalla strings.Builder
	texto, err := readPasswordFrom(strings.NewReader("sin-salto"), &pantalla)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "sin-salto" {
		t.Fatalf("texto = %q, queria %q", texto, "sin-salto")
	}
}

func TestReadPasswordFromAllowsEmptyPassword(t *testing.T) {
	var pantalla strings.Builder
	texto, err := readPasswordFrom(strings.NewReader("\n"), &pantalla)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if texto != "" {
		t.Fatalf("texto = %q, queria vacio", texto)
	}
}
