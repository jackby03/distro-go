// Package linuxiso lee una ISO live: localiza casper/filesystem.squashfs, extrae
// los hechos del volumen (compresor, bloque, ids, flags) y expone el rootfs como
// rootfs.Source.
//
// No escribe nada: ni temporales ni NTFS. El contenido se sirve en streaming con
// buffer acotado.
package linuxiso
