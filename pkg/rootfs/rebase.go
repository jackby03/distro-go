package rootfs

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrOutside indica que se pidio una ruta que no pertenece al subarbol.
var ErrOutside = errors.New("ruta fuera del subarbol")

// Rebase presenta el arbol de src colgando de prefix.
//
// Sirve para copiar un subarbol con la misma maquinaria que el arbol completo:
// poblar /home/<usuario> desde /etc/skel hereda asi el orden correcto de
// permisos, dueno y tiempos, sin duplicar esa logica (que es donde estan los
// errores que ya costaron caro con los bits setuid).
//
// El prefijo se normaliza al contrato: sin barra inicial ni final, asi que
// "home/ana" y "/home/ana/" producen las mismas rutas, siempre relativas.
//
// El subarbol no incluye su propia raiz: el directorio prefix lo crea quien
// llama, porque es el unico que sabe con que dueno y permisos debe quedar. Si
// Rebase lo entregara, Populate lo sobrescribiria con los de /etc/skel, que son
// root:root.
func Rebase(src Source, prefix string) Source {
	limpio := strings.Trim(prefix, "/")
	if limpio == "" {
		return src
	}
	return &rebased{src: src, prefix: limpio}
}

type rebased struct {
	src    Source
	prefix string
}

func (r *rebased) Walk(fn func(Entry) error) error {
	return r.src.Walk(func(e Entry) error {
		// La raiz del subarbol no se entrega nunca: la crea quien llama.
		if strings.Trim(e.Path, "/") == "" {
			return nil
		}
		e.Path = r.prefix + "/" + strings.Trim(e.Path, "/")
		return fn(e)
	})
}

func (r *rebased) Open(path string) (io.ReadCloser, error) {
	original, err := r.unjoin(path)
	if err != nil {
		return nil, err
	}
	return r.src.Open(original)
}

// unjoin devuelve la ruta del origen a partir de una del subarbol.
func (r *rebased) unjoin(path string) (string, error) {
	limpio := strings.Trim(path, "/")
	if strings.HasPrefix(limpio, r.prefix+"/") {
		return strings.TrimPrefix(limpio, r.prefix+"/"), nil
	}
	return "", fmt.Errorf("%w: %q no cuelga de %q", ErrOutside, path, r.prefix)
}
