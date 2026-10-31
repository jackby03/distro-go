//go:build !windows

package windisk

// IsElevated no tiene sentido fuera de Windows: aqui no hay discos fisicos que
// abrir. Se devuelve false para que ninguna ruta lo tome por un administrador.
func IsElevated() bool { return false }
