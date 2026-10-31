//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// ErrNoConsole indica que no hay terminal interactivo donde pedir la contrasena.
var ErrNoConsole = errors.New("no hay terminal interactivo para pedir la contrasena")

// promptPassword lee una contrasena del terminal sin mostrarla.
//
// Desactiva el eco y el modo linea, porque solo leyendo tecla a tecla se puede
// pintar un asterisco por caracter. Tambien desactiva el procesado de entrada
// para que Ctrl+C llegue como caracter y la cancelacion sea limpia en lugar de
// matar el proceso a media escritura.
//
// El modo original se restaura siempre, tambien si el usuario cancela.
func promptPassword(prompt string) (string, error) {
	h := windows.Handle(os.Stdin.Fd())

	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoConsole, err)
	}

	raw := mode &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if err := windows.SetConsoleMode(h, raw); err != nil {
		return "", fmt.Errorf("preparar el terminal: %w", err)
	}
	defer func() {
		// Si esto falla el usuario se queda escribiendo a la vista, pero no hay
		// nada mejor que hacer: se restauraria dentro de un defer.
		_ = windows.SetConsoleMode(h, mode)
	}()

	fmt.Fprint(os.Stderr, prompt)

	editor := newPasswordEditor(os.Stderr)
	var high rune

	for {
		var buf [1]uint16
		var read uint32
		if err := windows.ReadConsole(h, &buf[0], 1, &read, nil); err != nil {
			return "", fmt.Errorf("leer del terminal: %w", err)
		}
		if read == 0 {
			continue
		}

		r := rune(buf[0])
		// Los caracteres fuera del plano basico llegan partidos en dos mitades.
		if utf16.IsSurrogate(r) {
			if high == 0 {
				high = r
				continue
			}
			r, high = utf16.DecodeRune(high, r), 0
		}

		done, err := editor.feed(r)
		if err != nil {
			return "", err
		}
		if done {
			return editor.text(), nil
		}
	}
}
