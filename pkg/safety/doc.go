// Package safety decide si es seguro escribir en un disco.
//
// Bloquea solo por identidad: el disco del sistema Windows, un dispositivo de
// solo lectura, o un disco que no informa de serial o de tamano. Tener volumenes
// montados o particiones existentes no bloquea: se devuelve como aviso, con el
// detalle de lo que se va a destruir.
//
// La confirmacion no es interactiva: nombrar el disco (--target-disk) y su
// serial (--expect-serial), al estilo diskpart.
//
// Es politica pura, sin I/O: recibe un inventario y devuelve veredicto, para
// poder testearla con inventarios falsos.
package safety
