//go:build !windows

package blockdev

// InUse no se puede comprobar de forma portable fuera de Windows. En Linux el
// bloqueo es de rango (flock) y depende del sistema; para los tests no hace falta.
func InUse(path string) (bool, error) {
	_ = path
	return false, nil
}
