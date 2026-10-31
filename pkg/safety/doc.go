// Package safety decide si es seguro escribir en un disco: allowlist por
// número/serial/tamaño, denylist del disco de C:\ y de cualquier disco con
// volúmenes montados, y verificación de elevación.
//
// Es política pura, sin I/O: recibe un inventario y devuelve veredicto, para
// poder testearla con inventarios falsos.
package safety
