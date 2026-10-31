// Package linuxiso lee un ISO live.
//
// Abre la imagen ISO9660, localiza el kernel, el initrd y el rootfs comprimido de
// un sistema live (casper, live o LiveOS) y los sirve como lectores, sin extraer
// nada a disco: ni temporales ni NTFS de por medio.
//
// No interpreta el contenido del rootfs. Convertirlo en un arbol de ficheros
// POSIX es trabajo de pkg/rootfs, en M4.
package linuxiso
