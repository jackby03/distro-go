# Hallazgos del spike H-1

El spike se retiro del arbol. Este documento es lo unico que sobrevive de el, junto con
la salida de e2fsck que valido el resultado.

## Decision que tomo el spike

GO: go-diskfs (v1.9.4) produce ext4 estructuralmente valido desde Windows.

- e2fsck 1.47.4, cinco pasadas sin errores, en Alpine 3.24 dentro de QEMU.
- Imagen de 8 GiB con raiz `distrogo-root`.
- Resultado: 19986/40448 ficheros, 0,2 por ciento no contiguos, 431528/2097152 bloques.
- 12 binarios setuid presentes: sudo, su, passwd, mount, pkexec, chfn, chsh, gpasswd,
  newgrp, umount, fusermount3 y dbus-daemon-launch-helper.
- du -sh: 1,6 GiB. Label y UUID legibles.
- Modo y permisos correctos en la muestra revisada.

Consecuencia: H4 (M5 en el roadmap) deja de ser "escribir un writer ext4" y pasa a ser
"adaptar go-diskfs y parchear dos huecos".

## Numeros de referencia del rootfs de Mint 22.3 Xfce

Compresion xz, bloque de 128 KiB, squashfs version 4.0.

- Entradas totales: 507.623.
- Ficheros regulares: 322.477.
- Symlinks: 162.239.
- Directorios: 22.899.
- Device nodes: 8.
- Contenido: 6,58 GiB. Mayor fichero: usr/lib/thunderbird/libxul.so, 165 MiB.
- uid distintos: 18. gid distintos: 29.
- setuid: 18. setgid: 18.
- Hardlinks: 17 inodos que suman 38 entradas.
- xattrs: ninguno. El ISO no los lleva (flags 0x00C0, sin el bit 0x200).
- Walk de metadatos: 103 s, unos 200 microsegundos por entrada.
- Instalacion completa estimada: 10 a 15 minutos, a 24 MiB/s. El cuello es la
  descompresion xz en un solo hilo.

## Bugs de CalebQ42/squashfs v1.4.1 (MPL-2.0)

### 1. Deadlock y perdida de datos en FullReader.WriteTo

En low/data/fullreader.go, tras escribir un bloque hace `i++` y despues
`delete(results, i)`, con lo que borra el bloque siguiente en lugar del recien escrito.
Con resultados fuera de orden (siempre, porque usa runtime.NumCPU() workers) el
consumidor se queda esperando un bloque que ya nadie va a enviar. El proceso muere con
"all goroutines are asleep - deadlock". Se reprodujo a los pocos ficheros de un ISO real.

Solucion adoptada: no usar WriteTo. Leer con File.Read, que es secuencial y correcto, o
reimplementar el paralelismo sobre FullReader.Block(i). Coste: se pierde la descompresion
multihilo de la libreria.

### 2. Rutas anidadas rotas en Windows

El paquete usa filepath.Clean en fs.go, que en Windows convierte "/" en "\". Despues
hace strings.Split(name, "/"), asi que cualquier ruta con mas de un componente devuelve
fs.ErrNotExist.

Solucion adoptada: navegar componente a componente (OpenFile("usr"), luego File.FS(),
luego OpenFile("bin")). Es lo que debe hacer pkg/linuxiso.

### 3. Ficheros de 0 bytes ilegibles

data.NewReader llama a Block(0), y con BlockNum() igual a 0 devuelve "invalid block
index". Afectaba a 39 ficheros del rootfs.

Solucion adoptada: si Size es 0, crear el fichero vacio y no leer nada.

## Huecos de go-diskfs ext4 (v1.9.4)

- Mknod devuelve ErrNotImplemented. Afecta a 8 device nodes. Irrelevante en la practica
  porque /dev es tmpfs y systemd lo crea al arrancar.
- Link (hardlinks) devuelve ErrNotImplemented. Afecta a 21 entradas. Duplicar el
  contenido es valido para ext4 y e2fsck no se queja.
- Los dos son parcheables, en torno a 60 lineas, si algun dia molestan. La licencia es
  Apache-2.0, sin ataduras.

## Lo que reutilizan los milestones siguientes

- pkg/rootfs (contrato Source y Target) salio de aqui y es codigo de producto.
- El adaptador de squashfs a rootfs.Source se reescribira limpio en pkg/linuxiso (M4).
