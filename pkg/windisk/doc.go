// Package windisk es la capa Windows: enumeración de discos, apertura de
// \.\PhysicalDriveN, consulta de bus/serial/tamaño de sector y commit de la
// tabla GPT vía IOCTL_DISK_SET_DRIVE_LAYOUT_EX.
//
// Lo específico de Windows vive en ficheros *_windows.go; este doc.go no lleva
// etiqueta de build para que ./... siga compilando en Linux.
package windisk
