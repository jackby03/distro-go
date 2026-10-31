//go:build !windows

package windisk

import "errors"

// ErrUnsupported indica que la enumeracion de discos solo existe en Windows.
var ErrUnsupported = errors.New("la enumeracion de discos fisicos solo esta disponible en Windows")

// Enumerate no esta disponible fuera de Windows. Existe para que `go build
// ./...` y `go vet ./...` funcionen en Linux, donde corren los paquetes puros.
func Enumerate() (Inventory, error) {
	return Inventory{}, ErrUnsupported
}
