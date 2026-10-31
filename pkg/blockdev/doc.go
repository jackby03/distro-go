// Package blockdev define el acceso a un dispositivo de bloques (o a un fichero
// que lo simula) con una interfaz mínima: ReadAt, WriteAt, Sync, Size y
// SectorSize.
//
// Es la frontera que permite probar todo el pipeline (GPT, FAT32, ext4) contra
// un .img sin privilegios en Windows y sin tocar hardware.
package blockdev
