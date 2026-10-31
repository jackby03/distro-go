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
- Arnes de pruebas: QEMU mas Alpine para e2fsck y fsck.fat. Sin WSL y sin Docker.
- Fixtures: Mint 22.3 (casper) primario, Debian live secundario, Alpine solo como caso
  de "layout raro" (su rootfs va en el initramfs, no en squashfs).
- Hardware: Disk 3 (USB, 466 GiB) y solo en M7. Disk 2 tambien es USB y contiene datos.
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

- [ ] M2 — GPT sobre imagen
  - Entrega: particionado de un .img con ESP de 512 MiB y root con el resto, con la
    tabla publicada al final (commit-last).
  - Ficheros: pkg/gpt y el comando de particionado.
  - Respuestas: ~3.
  - Criterio de salida: `sfdisk -l test.img` muestra dos particiones, los CRC de GPT
    son correctos y la operacion se puede repetir sin corromper la imagen.

- [ ] M3 — FAT32, ESP e ISO live
  - Entrega: formateo FAT32 del ESP, copia del shim y GRUB del ISO, grub.cfg generado
    y arranque de un sistema live persistente.
  - Ficheros: pkg/fat32, pkg/efilayout, pkg/linuxiso (version 1: ISO9660 y deteccion
    de casper).
  - Respuestas: ~5.
  - Criterio de salida: QEMU con OVMF arranca Mint live desde la imagen generada.
  - Bloqueado por DG-01 (falta OVMF).

- [ ] M4 — Lector de SquashFS
  - Entrega: pkg/linuxiso completo, con los tres workarounds documentados, y
    extraccion del arbol a un directorio para poder testear.
  - Ficheros: adaptador squashfs, walker y tests.
  - Respuestas: ~3.
  - Criterio de salida: el arbol extraido valida contra casper/filesystem.manifest,
    las 507.623 entradas aparecen y el proceso tarda menos de 5 minutos.

- [ ] M5 — pkg/ext4fs
  - Entrega: formateo y poblado de ext4 sobre una particion del .img, usando go-diskfs
    y los parches que decida DG-04.
  - Ficheros: ext4fs, populador y tests.
  - Respuestas: ~3.
  - Criterio de salida: e2fsck sin errores sobre /dev/vdb2, montaje correcto,
    12 binarios setuid y uid/gid no root con los valores esperados.

- [ ] M6 — Instalacion completa en imagen
  - Entrega: plan y apply de punta a punta sobre test.img, con diario de fases, y
    arranque UEFI del sistema instalado.
  - Ficheros: internal/app (plan, apply, verify) y el journal.
  - Respuestas: ~4.
  - Criterio de salida: QEMU con OVMF llega a la pantalla de login del sistema
    instalado, no del live.
  - Bloqueado por DG-01.

- [ ] M7 — Primeras escrituras a hardware real
  - Entrega: commit de la tabla GPT y volcado sobre \\.\PhysicalDrive3 con el guard
    activo.
  - Ficheros: pkg/windisk (IOCTLs de escritura) y el guard definitivo.
  - Respuestas: ~3.
  - Criterio de salida: el sistema arranca en el portatil por USB y el guard rechaza
    Disk 0, Disk 1 y Disk 2.
  - Bloqueado por DG-02.

## Cadena de dependencias

```
M1 (blockdev + windisk + safety)     -> habilita M7
M2 (gpt)                             -> habilita M3
M3 (linuxiso v1 + fat32 + efilayout) -> habilita M6
M4 (squashfs) + M5 (ext4fs)          -> habilitan M6
```

M1 y M2 son independientes entre si y se pueden paralelizar si hay prisa.

## Como se actualiza

- Al cerrar un milestone se cambia `[ ]` por `[X]` y se anota la fecha y la evidencia
  en backlog.md.
- Si cambia un criterio de salida, se cambia aqui y se cita el DG que lo motivo.
