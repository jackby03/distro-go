//go:build !windows

package cli

import "fmt"

// runFlash existe fuera de Windows solo para que el paquete compile.
func (a *App) runFlash(args []string) int {
	fmt.Fprintln(a.Err, "error: escribir en un disco fisico solo esta disponible en Windows")
	return exitError
}
