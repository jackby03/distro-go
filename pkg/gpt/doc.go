// Package gpt construye y valida tablas GPT en memoria: MBR protector, cabecera
// primaria y de respaldo, entradas de partición y CRC32.
//
// No conoce Win32: serializa bytes. El commit al dispositivo y el refresco de la
// tabla lo hace pkg/windisk.
package gpt
