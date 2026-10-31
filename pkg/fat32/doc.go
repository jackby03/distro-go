// Package fat32 formatea y escribe un volumen FAT32.
//
// Se usa para el ESP, donde la firma UEFI solo entiende FAT. Tres ficheros, por
// orden de lectura:
//
//   - fat32.go: geometria, BPB, FSInfo y tabla FAT.
//   - name.go: nombres 8.3 generados y entradas LFN.
//   - write.go: directorios, ficheros y cadenas de clusters.
//
// No implementa rootfs.Target: FAT32 no guarda uid, gid, modos POSIX ni
// symlinks, asi que fingir el contrato seria mentir. Su API es minima:
// formatear, crear directorios y crear ficheros.
//
// Los cambios viven en memoria hasta que se llama a Close, que vuelca la FAT y
// recalcula el contador de clusters libres de la FSInfo.
package fat32
