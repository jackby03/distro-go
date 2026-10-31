package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// ErrPromptInterrupted indica que el usuario cancelo la lectura con Ctrl+C.
var ErrPromptInterrupted = errors.New("lectura cancelada por el usuario")

// passwordMask es el caracter de feedback por cada tecla.
const passwordMask = '*'

// passwordEditor acumula los caracteres de una contrasena sin mostrarlos.
//
// Esta separado del terminal a proposito: recibe caracteres uno a uno y solo
// escribe asteriscos en la salida. Asi la logica de edicion se puede testear sin
// consola, que es donde de verdad hay casos raros (borrado, teclas de control,
// cancelacion).
type passwordEditor struct {
	out   io.Writer
	runes []rune

	// skipNext descarta el codigo de tecla que sigue al prefijo 0x00 o 0xE0 que
	// mete la consola de Windows en las teclas extendidas (flechas, Inicio...).
	skipNext bool
	// escape dice en que punto de una secuencia ESC [ ... vamos, para las
	// terminales que hablan en ANSI en lugar de mandar codigos de tecla.
	escape int
}

func newPasswordEditor(out io.Writer) *passwordEditor {
	return &passwordEditor{out: out}
}

// feed procesa un caracter y dice si la lectura ha terminado.
func (p *passwordEditor) feed(r rune) (done bool, err error) {
	// Teclas extendidas de la consola: 0x00 o 0xE0 seguido de un codigo de
	// tecla. El codigo se descarta, si no acabaria dentro de la contrasena.
	if p.skipNext {
		p.skipNext = false
		return false, nil
	}
	if r == 0x00 || r == 0xE0 {
		p.skipNext = true
		return false, nil
	}

	// Secuencias ANSI: ESC, el introductor ([ u O) y parametros hasta la letra
	// final. Se tragan enteras.
	switch p.escape {
	case 1:
		if r == '[' || r == 'O' {
			p.escape = 2
		} else {
			p.escape = 0
		}
		return false, nil
	case 2:
		if r >= 0x40 && r <= 0x7e {
			p.escape = 0
		}
		return false, nil
	}

	switch r {
	case '\r', '\n':
		fmt.Fprintln(p.out)
		return true, nil

	case 0x1b: // ESC: puede ser una secuencia o un caracter suelto
		p.escape = 1
		return false, nil

	case 0x03: // Ctrl+C
		fmt.Fprintln(p.out)
		return false, ErrPromptInterrupted

	case '\b', 0x7f: // Backspace y Supr
		if len(p.runes) == 0 {
			return false, nil
		}
		p.runes = p.runes[:len(p.runes)-1]
		// Borra el asterisco de la pantalla, no solo del buffer.
		fmt.Fprint(p.out, "\b \b")
		return false, nil
	}

	// Cualquier otra tecla de control (tabulador, inicio) se ignora: no forma
	// parte de una contrasena y no debe dejar rastro.
	if r < 0x20 {
		return false, nil
	}

	p.runes = append(p.runes, r)
	fmt.Fprint(p.out, string(passwordMask))
	return false, nil
}

// text devuelve lo tecleado hasta el momento.
func (p *passwordEditor) text() string {
	return string(p.runes)
}

// readPasswordFrom lee una contrasena de un flujo sin consola: entradas
// redirigidas y tests.
//
// No puede ocultar el eco, asi que solo se usa cuando no hay terminal y quien
// llama avisa de ello.
func readPasswordFrom(r io.Reader, out io.Writer) (string, error) {
	editor := newPasswordEditor(out)
	br := bufio.NewReader(r)

	for {
		c, _, err := br.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return editor.text(), nil
			}
			return "", fmt.Errorf("leer la contrasena: %w", err)
		}

		done, err := editor.feed(c)
		if err != nil {
			return "", err
		}
		if done {
			return editor.text(), nil
		}
	}
}

// intentosPassword es cuantas veces se pide la contrasena antes de rendirse.
const intentosPassword = 3

// readNewPassword pide una contrasena nueva, dos veces, y comprueba que coincide.
//
// Se pide dos veces porque no se ve lo que se escribe: sin la comprobacion, una
// tecla de mas deja al usuario con una contrasena que no conoce y un sistema al
// que no puede entrar. ask se inyecta para poder probar los reintentos sin
// consola.
func readNewPassword(name string, ask func(string) (string, error), out io.Writer) (string, error) {
	var ultimo error

	for intento := 1; intento <= intentosPassword; intento++ {
		clave, err := ask(fmt.Sprintf("Contrasena para %s: ", name))
		if err != nil {
			return "", err
		}
		if clave == "" {
			ultimo = errors.New("la contrasena esta vacia")
			fmt.Fprintln(out, "error: la contrasena no puede estar vacia")
			continue
		}

		repetida, err := ask("Repite la contrasena: ")
		if err != nil {
			return "", err
		}
		if clave != repetida {
			ultimo = errors.New("las dos contrasenas no coinciden")
			fmt.Fprintln(out, "error: no coinciden, intentalo otra vez")
			continue
		}
		return clave, nil
	}

	return "", fmt.Errorf("no se pudo leer la contrasena: %w", ultimo)
}
