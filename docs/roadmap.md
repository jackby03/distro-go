# Roadmap — DistroGo

Estado vivo del proyecto. Se actualiza al cerrar cada milestone.
Aqui esta el QUE. El COMO (protocolo, estandares, DoD) esta en AGENTS.md.
Los issues concretos estan en backlog.md.

## Reglas

1. Un milestone no se cierra sin el DoD de AGENTS.md (seccion 6) y sin el visto bueno
   explicito del humano.
2. Ninguna escritura a \\.\PhysicalDrive antes de M7. Todo se valida contra un .img
   y QEMU.
3. Ritmo: un fichero logico por respuesta, con parada. La linea "Respuestas" es la
   estimacion de turnos que cuesta el milestone.
4. Si un milestone se bloquea se abre un DG-xx en backlog.md y se sigue con lo que no
   este bloqueado.

## Decisiones cerradas

No se re-discuten sin abrir un DG-xx que lo justifique.

- Lenguaje: Go, CGO_ENABLED=0, GOARCH=amd64. La toolchain de la maquina es 386.
- ext4: Plan A, go-diskfs mas parches puntuales. El spike lo valido con e2fsck limpio.
- SquashFS: CalebQ42/squashfs mas tres workarounds. Es MPL-2.0, se envuelve y no se
  modifica. Si algun dia se parchea, las modificaciones hay que publicarlas.
- Layout: pkg/ son librerias reutilizables, internal/ es orquestacion no exportable.
- CLI: `distrogo list` y `distrogo install --iso --target-disk --hostname`.
- Arnes de pruebas: QEMU mas Alpine para e2fsck y fsck.fat, lanzado con tools/lab-qemu.ps1. Sin WSL y sin Docker.
- Fixtures: Mint 22.3 (casper) primario, Debian live secundario, Alpine solo como caso
  de "layout raro" (su rootfs va en el initramfs, no en squashfs).
- Hardware: Disk 3 (USB, 466 GiB) y solo en M7. Disk 2 tambien es USB y contiene datos.
- Arranque instalado: no depende de casper ni de live-boot, solo necesita kernel,
  initrd y `root=UUID`. Las variantes solo importan para detectar donde estan las
  piezas dentro del ISO.
- Alcance de disco: una instalacion por disco y disco dedicado. DistroGo no
  comparte disco con otros sistemas operativos, ni conserva particiones ajenas.
  No es una prohibicion: es que no hay necesidad para el caso de uso (un disco
  externo que se dedica entero a la distro) y compartir disco complica el
  particionado y el arranque. Si algun dia hace falta, se abre un DG-xx.
- Interfaz: la beta es CLI porque permite probar el motor con precisión. La
  version estable sera una GUI al estilo Rufus o balenaEtcher. Por eso el nucleo
  del particionado sera un **perfil declarativo**: los flags de hoy y la GUI de
  mañana son dos frontends del mismo fichero, y el camino destructivo siempre
  pasa por un plan revisable.
- Swap: depende del bus del disco destino. En disco externo (USB, USB-C) no se
  crea swap: el dispositivo se degrada y el intercambio es lentisimo, asi que el
  sistema trabaja solo con RAM. En disco interno (SATA, NVMe) si tiene sentido.
  Hoy no se crea swap en ningun caso; implementarlo es de M9.
- Camino a hardware: construir la imagen entera con `partition` y copiarla con
  `flash` en secuencia. Escribir el sistema de ficheros directamente sobre el disco
  se midio en 0,6 entradas/s en un HDD por USB, porque cada fichero son decenas de
  accesos aleatorios y Windows no cachea escrituras en discos extraibles. La imagen
  se construye donde lo aleatorio es barato.
- Antivirus: Defender bloquea `FSCTL_LOCK_VOLUME` e `IOCTL_VOLUME_OFFLINE` en
  procesos sin reputacion. Requisito documentado para el PoC: exclusion de proceso
  para `distrogo.exe`. La version estable necesita firma de codigo (DG-32).
- Politica de disco: solo bloquea el disco del sistema (IsSystem/IsBoot o que aloje
  C:), el dispositivo de solo lectura y el disco sin serial o sin tamano. Tener
  volumenes montados o particiones no bloquea: se avisa de lo que se pierde. La
  confirmacion son las flags, al estilo diskpart: --target-disk y --expect-serial.

## Milestones

- [X] M-1 — Spike de viabilidad de librerias
  - Entrega: medir go-diskfs y squashfs contra el ISO real de Mint.
  - Respuestas: n/a. Codigo borrado del arbol, hallazgos en docs/library-findings.md.
  - Criterio de salida: e2fsck sin errores en QEMU sobre una imagen de 20.000 entradas.

- [X] M0 — Esqueleto del proyecto
  - Entrega: repo git, .gitignore, AGENTS.md, docs, skills y contrato pkg/rootfs.
  - Respuestas: n/a.
  - Criterio de salida: commit inicial. Cumplido.

- [X] M1 — `distrogo list`
  - Entrega: inventario de solo lectura con bus, serial, sectores y veredicto de
    proteccion por disco.
  - Ficheros: pkg/blockdev, pkg/windisk, pkg/safety, internal/cli, cmd/distrogo.
  - Respuestas: ~5.
  - Criterio de salida: lista los cuatro discos; solo Disk 1 aparece bloqueado
    (aloja Windows); Disk 2 y Disk 3 aparecen elegibles con el aviso de lo que se
    destruiria; no escribe nada y funciona sin admin. Cumplido y aprobado.

- [X] M2 — GPT sobre imagen
  - Entrega: particionado de un .img con ESP de 512 MiB y root con el resto, con la
    tabla publicada al final (commit-last).
  - Ficheros: pkg/gpt y el comando de particionado.
  - Respuestas: ~3.
  - Criterio de salida: `sfdisk -l test.img` muestra dos particiones, los CRC de GPT
    son correctos y la operacion se puede repetir sin corromper la imagen.
    Cumplido: sfdisk 2.42.3 en Alpine confirma GPT, dos particiones, header 1.0,
    2 de 128 entradas y "No errors detected".

- [X] M3 — FAT32, ESP e ISO live
  - Entrega: formateo FAT32 del ESP, copia del shim y GRUB del ISO, grub.cfg generado
    y arranque de un sistema live persistente.
  - Ficheros: pkg/fat32, pkg/efilayout, pkg/linuxiso (version 1: ISO9660 y deteccion
    de casper).
  - Respuestas: ~5.
  - Criterio de salida: QEMU con OVMF arranca Mint live desde la imagen generada.
    Cumplido: shim -> GRUB con nuestro grub.cfg -> kernel -> initrd -> casper ->
    squashfs -> escritorio de Mint. La persistencia queda pendiente de ext4 (DG-27).

- [X] M4 — Lector de SquashFS
  - Entrega: pkg/linuxiso completo, con los tres workarounds documentados, y
    extraccion del arbol a un directorio para poder testear.
  - Ficheros: adaptador squashfs, walker y tests.
  - Respuestas: ~3.
  - Criterio de salida: el arbol extraido valida contra casper/filesystem.manifest,
    las 507.623 entradas aparecen y el proceso tarda menos de 5 minutos.
    Cumplido: 507.623 entradas recorridas en 97 s, con los tres workarounds de
    docs/library-findings.md (WriteTo, filepath.Clean y ficheros de 0 bytes).

- [X] M5 — pkg/ext4fs
  - Entrega: formateo y poblado de ext4 sobre una particion del .img, usando go-diskfs
    y los parches que decida DG-04.
  - Ficheros: ext4fs, populador y tests.
  - Respuestas: ~3.
  - Criterio de salida: e2fsck sin errores sobre /dev/vdb2, montaje correcto,
    12 binarios setuid y uid/gid no root con los valores esperados.
    Cumplido: 507625/560096 ficheros, 0,1 por ciento no contiguos, sin errores.

- [X] M6 — Instalacion completa en imagen
  - Entrega: `partition --install` hace GPT, ESP, ext4, volcado y arranque en una
    sola pasada, con marcas de fase y progreso con ritmo y ETA. El comando `boot`
    reescribe por separado el arranque, que es lo que permite iterar en 1,4 s sin
    repetir el volcado.
  - Ficheros: internal/cli (partition, install, boot), pkg/bootcfg,
    pkg/ext4fs/read.go y el candado de pkg/blockdev.
  - Respuestas: ~4.
  - Criterio de salida: QEMU con OVMF llega a la pantalla de login del sistema
    instalado, no del live.
    Cumplido: 507.615 entradas y 7.070.158.975 bytes en 45m7s; grub.cfg con
    root=UUID y fstab por UUID; el invitado llega al greeter sin icono de
    instalador y se inicia sesion en el escritorio. Para entrar hubo que crear la
    cuenta a mano, porque el ISO no trae ninguna: DG-30.

- [ ] M6.5 — Cuenta de usuario en la instalacion
  - Entrega: escribir /etc/passwd, /etc/shadow y /etc/group para la cuenta del
    usuario, con home desde /etc/skel y los grupos administrativos, para que el
    sistema instalado se pueda usar sin editar la linea de GRUB.
  - Ficheros: pkg/bootcfg (o pkg/accounts, segun la decision) e install.go.
  - Respuestas: ~2.
  - Criterio de salida: el invitado arranca, se inicia sesion con la cuenta creada
    por la herramienta y sudo funciona sin configuracion manual.
  - Decision pendiente: cuenta por parametros frente a creacion en el primer
    arranque estilo OEM.

- [ ] M7 — Primeras escrituras a hardware real
  - Entrega: commit de la tabla GPT y volcado sobre \\.\PhysicalDrive3 con el guard
    activo.
  - Ficheros: pkg/windisk (IOCTLs de escritura) y el guard definitivo.
  - Respuestas: ~3.
  - Criterio de salida: el sistema arranca en el portatil por USB y el guard rechaza
    Disk 0, Disk 1 y Disk 2.
  - Bloqueado por DG-02.

- [ ] M8 — Rendimiento del volcado
  - Entrega: bajar el coste por entrada del populador.
  - Ficheros: pkg/rootfs y pkg/ext4fs/target.go.
  - Respuestas: ~4.
  - Criterio de salida: Mint 22.3 completa en menos de 20 minutos, con e2fsck
    limpio y el mismo numero de entradas que hoy.
  - Medido en M6: 507.615 entradas en 2.707 s, 187 entradas/s (DG-28).

- [ ] M9 — Perfil declarativo de particiones (futuro)
  - Entrega: describir el layout en un fichero (disco por serial, tabla, lista de
    particiones con rol, tamano relativo o absoluto, formato y punto de montaje),
    con `plan` que resuelve los numeros sin escribir y `apply` que ejecuta.
  - Incluye: validar que el rootfs cabe antes de tocar nada, alineacion a 1 MiB, y
    swap segun el bus del disco destino.
  - Ficheros: pkg/gpt, un pkg/partitionplan nuevo, y la CLI.
  - Respuestas: ~6.
  - Criterio de salida: dos perfiles (externo sin swap, interno con swap) instalan
    la misma ISO con `apply --plan`, y un plan que no cabe se rechaza sin escribir.

- [ ] M10 — GUI sobre el perfil (futuro)
  - Entrega: interfaz grafica al estilo Rufus o balenaEtcher que genera el perfil
    de M9 y lo entrega al mismo motor. No es un TUI ni una CLI interactiva.
  - Respuestas: n/a hasta que M9 cierre.
  - Criterio de salida: instalar desde la GUI produce un perfil byte a byte igual
    al que se escribiria a mano con los mismos datos.

- [ ] M11 — Discos de 4K logicos (futuro, lejano)
  - Entrega: soportar sectores logicos de 4096 bytes ademas de 512.
  - Motivo: `ext4fs.Open` rechaza hoy cualquier sector que no sea de 512, y el
    resto de conversiones LBA-a-bytes asumen lo mismo. Los discos externos de
    4Kn no funcionarian.
  - Fuera de alcance de la beta: el laboratorio usa un HDD USB con sectores de
    512, y las memorias USB tampoco estan en el alcance.
  - Respuestas: ~4.

## Cadena de dependencias

```
M1 (blockdev + windisk + safety)     -> habilita M7
M2 (gpt)                             -> habilita M3
M3 (linuxiso v1 + fat32 + efilayout) -> habilita M6
M4 (squashfs) + M5 (ext4fs)          -> habilitan M6
M6 (arranque instalado)              -> habilita M6.5 y M7
M6 (instalacion completa)            -> permite medir M8
M9 (perfil de particiones)           -> habilita M10
M11 (sectores de 4K)                 -> condiciona los discos que se pueden usar
```

M1 y M2 son independientes entre si y se pueden paralelizar si hay prisa.

## Como se actualiza

- Al cerrar un milestone se cambia `[ ]` por `[X]` y se anota la fecha y la evidencia
  en backlog.md.
- Si cambia un criterio de salida, se cambia aqui y se cita el DG que lo motivo.
