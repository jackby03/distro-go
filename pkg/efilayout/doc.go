// Package efilayout prepara el árbol de arranque en la ESP: copia el shim y GRUB
// firmados que vienen en la ISO y genera el grub.cfg de la instalación.
//
// No fabrica binarios EFI: reutiliza los firmados por la distribución.
package efilayout
