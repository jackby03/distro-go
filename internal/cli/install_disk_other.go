//go:build !windows

package cli

import "fmt"

// runInstall existe fuera de Windows solo para que el paquete compile: escribir
// en un disco fisico es cosa de las API de Windows.
func (a *App) runInstall(args []string) int {
	fmt.Fprintln(a.Err, "error: instalar en un disco fisico solo esta disponible en Windows")
	return exitError
}
