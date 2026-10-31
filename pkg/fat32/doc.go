// Package fat32 formatea y escribe un volumen FAT32.
//
// Se usa para el ESP, donde la firma UEFI solo entiende FAT. La primera mitad
// del paquete, el formateo, construye el BPB, la FSInfo, las dos copias de la
// tabla FAT y el directorio raiz. La segunda, la escritura de ficheros y
// directorios con nombres largos, vive en write.go.
//
// No implementa rootfs.Target: FAT32 no guarda uid, gid, modos POSIX ni
// symlinks, asi que fingir el contrato seria mentir. Su API es minima:
// formatear, crear directorios y crear ficheros.
//
// TODO(DG-24): falta la segunda mitad: directorios, ficheros y su cadena de
// clusters. Los nombres, 8.3 y LFN, ya estan en name.go.
package fat32
