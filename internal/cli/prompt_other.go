//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"os"
)

// ErrNoConsole indica que no hay terminal interactivo donde pedir la contrasena.
var ErrNoConsole = errors.New("no hay terminal interactivo para pedir la contrasena")

// promptPassword lee una contrasena del flujo estandar en los sistemas donde
// DistroGo no se ejecuta. Aqui no se puede silenciar el eco, asi que se avisa.
func promptPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fmt.Fprintln(os.Stderr, "(aviso: en este sistema el eco no se oculta)")

	text, err := readPasswordFrom(os.Stdin, os.Stderr)
	if err != nil {
		return "", err
	}
	return text, nil
}
