# Backlog — DistroGo

Issues de trabajo. Un DG-xx describe un problema con un criterio de cierre verificable.
Los milestones estan en roadmap.md.

## Convencion

- ID: DG-nn, correlativo. Nunca se reutiliza.
- Se marca `[X]` cuando esta cerrado, con la evidencia al lado.
- Cierre: se exige evidencia, no opinion. Comando y salida, o test que falla antes y
  pasa despues.
- Un issue que cambie una decision cerrada del roadmap debe citarla.

## Abiertos

- [ ] DG-01 — Falta OVMF/edk2 (M3, M6). BLOQUEANTE.
  - QEMU 11.1 esta instalado pero sin ficheros .fd, asi que no hay arranque UEFI.
  - Cierre: `qemu-system-x86_64` arranca en modo UEFI con edk2-x86_64-code.fd y
    edk2-i386-vars.fd y llega al menu de arranque de la imagen.

- [ ] DG-02 — Estabilidad del serial del Disk 3 (M7). BLOQUEANTE.
  - La allowlist del guard depende de que `201703310007F` no cambie tras replug ni tras
    reiniciar. Sin verificar.
  - Cierre: el serial es identico en tres lecturas separadas por un replug fisico.

- [ ] DG-03 — Implementar los tres workarounds de CalebQ42/squashfs (M4).
  - Deadlock de WriteTo, rutas anidadas rotas en Windows y ficheros de 0 bytes. Detalle
    y causas en docs/library-findings.md.
  - Cierre: extraccion completa del rootfs de Mint sin errores ni bloqueos.

- [ ] DG-04 — Decidir Mknod y Link de go-diskfs (M5).
  - Ambos devuelven ErrNotImplemented. Afecta a 8 device nodes y 21 hardlinks.
  - Cierre: decision escrita (parchear, que es Apache-2.0, o aceptar la perdida) y
    reflejada en el criterio de salida de M5.

- [ ] DG-05 — `--allow-non-usb` debe seguir siendo seguro (M1).
  - Aunque se pase el flag, el guard no puede permitir el disco de C:\, los marcados
    como IsSystem o IsBoot, ni discos con volumenes montados.
  - Cierre: test que pasa el flag y aun asi recibe rechazo para Disk 1.

- [ ] DG-06 — La identidad del disco va por serial (M1).
  - Hay dos discos USB y Disk 2 tiene datos del usuario. Filtrar solo por BusType es
    insuficiente.
  - Cierre: sin --expect-serial coincidente no se escribe en ningun disco, incluido
    Disk 2, y al confirmarlo se muestran los volumenes que se destruyen.

- [ ] DG-07 — Coste del walk de metadatos (M4).
  - 103 s para 507.623 entradas, unos 200 microsegundos por entrada.
  - Cierre: medido si el cuello esta en la libreria, y decision escrita (cache o
    aceptar) con el numero que la justifica.

- [ ] DG-08 — Descompresion xz en un solo hilo (M4, M5).
  - 24 MiB/s efectivos. Paralelizar sobre FullReader.Block(i) es posible sin modificar
    la libreria.
  - Cierre: medicion antes y despues, o decision de no hacerlo con el motivo.

- [ ] DG-09 — Falta el fichero LICENSE (M0).
  - El README declara MIT y enlaza a un fichero que no existe.
  - Cierre: `LICENSE` en la raiz con el texto MIT y el año y titular correctos.

- [ ] DG-11 — `distrogo list` sin elevacion (M1).
  - Debe funcionar con IOCTLs de solo consulta.
  - Cierre: ejecutado desde una consola sin privilegios y lista los cuatro discos.

- [ ] DG-12 — Debian 12 live como segunda fixture (M4).
  - Layout live/ en vez de casper/ e initrd.gz en vez de initrd.lz.
  - Cierre: la deteccion de distribucion reconoce el ISO de Debian y no confunde rutas.

- [ ] DG-13 — CI para los paquetes puros (post-M2).
  - pkg/blockdev, pkg/gpt y pkg/safety no dependen de Win32 y pueden correr en Linux.
  - Cierre: workflow que ejecuta `go test ./pkg/...` con CGO deshabilitado.

- [ ] DG-14 — Documentar el ABI medido de windisk (M1).
  - La cabecera de DRIVE_LAYOUT_INFORMATION_EX son 48 bytes, no 24, y
    PARTITION_INFORMATION_EX tiene el offset en +8 y la union en +32. Los tamaños
    coinciden con la documentacion pero el orden de campos no.
  - Cierre: `docs/windisk-abi.md` con la tabla de offsets, la evidencia (disco vacio
    devuelve 48 bytes) y referencia al test dorado que lo fija.

- [ ] DG-15 — Partir `inventory_windows.go` (M2).
  - 473 lineas y creciendo: apertura y consultas de disco, mas enumeracion de
    unidades y mapeo de volumenes, en el mismo fichero.
  - Cierre: `disks_windows.go` y `volumes_windows.go`, sin cambio de comportamiento.

- [ ] DG-17 — IsSystem e IsBoot reales (M7).
  - Hoy ambos se marcan en el disco de %SystemRoot%: es una aproximacion
    conservadora. La distincion real entre disco de sistema y de arranque esta en
    la BCD.
  - Cierre: leer la BCD o documentar que la aproximacion es definitiva.

- [ ] DG-18 — Imagenes .img dispersas en NTFS (M2).
  - Truncar en NTFS reserva el espacio real: una imagen de 466 GiB ocupa 466 GiB.
    Para el banco de pruebas con imagenes de 8-12 GiB no molesta; para replicar el
    disco objetivo, si.
  - Cierre: FSCTL_SET_SPARSE en el backend de fichero, con medicion de espacio real.

- [ ] DG-19 — Camino elevado de windisk sin ejercitar (M1).
  - Con la consola sin privilegios se usa el fallback de acceso 0; la rama
    GENERIC_READ no se ejecuta en esta maquina. La cobertura del 80,7 por ciento
    esconde ese hueco.
  - Cierre: una ejecucion como administrador que liste los discos y sin avisos.

## Abiertos (continuacion)

- [ ] DG-20 — Bloquear y desmontar los volumenes del disco objetivo (M7).
  - Con la politica corregida un disco montado es elegible. Para escribir la tabla
    hace falta FSCTL_LOCK_VOLUME y FSCTL_DISMOUNT_VOLUME en cada volumen del disco,
    y despues IOCTL_DISK_UPDATE_PROPERTIES.
  - Puede fallar si Explorer o un antivirus tienen ficheros abiertos: el mensaje
    debe decir que cerrar, no pedir mountvol.
  - Cierre: instalacion completa en un disco con volumenes montados sin pasos manuales.

- [ ] DG-21 — README y AGENTS.md coherentes con la politica nueva (M1).
  - El README afirma que la operacion falla cerrada salvo bus USB o --allow-non-usb.
    Ya no es cierto: solo bloquea el disco del sistema y --allow-non-usb silencia
    un aviso.
  - Cierre: lineas actualizadas en README y en la seccion de seguridad.

## Cerrados

- [X] M2 — GPT sobre imagen validado con una herramienta externa.
  - Evidencia: sfdisk 2.42.3 en Alpine, sin red, sobre out/test.img de 8 GiB.
    Disklabel gpt, Disk identifier 928d4912-3120-4c5f-ad86-9118bd1663fa,
    /dev/vda1 2048..1050623 (512M, EFI System) y /dev/vda2 1050624..16777182
    (7.5G, Linux filesystem); `sfdisk --verify` responde "No errors detected",
    header version 1.0 y 2 de 128 entradas.

- [X] DG-10 — .gitattributes con finales de linea normalizados.
  - Evidencia: `* text=auto eol=lf`, binarios marcados y formatos con convencion
    propia (ps1/bat en CRLF, sh en LF).

- [X] DG-23 — `distrogo partition` sobre imagen.
  - Evidencia: genera out/test.img de 8 GiB con ESP de 512 MiB en LBA 2048 y root
    hasta el final del area util; MBR protector 0xEE en el byte 450, firma 55 AA y
    sectores 0x00FFFFFF; verificacion con Read antes de salir. Falta la lectura con
    `sfdisk` en QEMU para cerrar M2.

- [X] M1 — distrogo list sobre hardware real.
  - Evidencia: 49 pruebas en verde; salida real con cuatro discos, Disk 1 bloqueado
    por alojar Windows, Disk 2 y Disk 3 elegibles y destructivos, ejecutado sin
    elevacion; `list --json` con codigos de motivo.

- [X] DG-16 — Un volumen montado ya no bloquea la instalacion.
  - Decision: solo bloquea el disco del sistema. Tener volumenes montados o
    particiones es un aviso con el detalle de lo que se destruye, no un rechazo.
    La confirmacion son las flags, al estilo diskpart. El bloqueo y desmontaje de
    volumenes pasa a ser un paso interno del instalador (DG-20).
  - Evidencia: TestAssessWarnsButAllowsMountedVolume y
    TestDevelopmentMachineOnlySystemDiskIsBlocked.

- [X] Viabilidad de escribir ext4 desde Windows.
  - Evidencia: e2fsck 1.47.4 con cinco pasadas sin errores, 12 binarios setuid, 1,6 GiB
    y label correcto. Detalle en docs/library-findings.md.
- [X] Retirar el validador casero del spike.
  - Evidencia: era un oraculo autorreferencial (go-diskfs leyendo lo que go-diskfs
    escribio). Eliminado; e2fsck queda como unico oraculo.

- [ ] DG-22 — Etiqueta y sistema de ficheros ausentes con acceso 0 (M1).
  - `GetVolumeInformationByHandle` sobre un handle abierto con acceso 0 falla en
    algunas unidades: en la maquina de desarrollo, D: sale sin sistema de ficheros
    aunque es NTFS. Las demas unidades si responden.
  - Cierre: reintento con GENERIC_READ o motivo documentado de por que no se puede.


- [ ] DG-24 — Escritura de ficheros y nombres largos en FAT32 (M3).
  - El formateo esta hecho. Falta crear directorios y ficheros, con entradas
    8.3 generadas y entradas LFN (secuencia de 13 caracteres, checksum del nombre
    corto y byte de secuencia), y crecimiento de directorios.
  - Cierre: copiar EFI/boot y boot/grub del ISO al ESP, y `fsck.fat -n` limpio con
    `mdir`/`mtype` de mtools leyendo los ficheros.
