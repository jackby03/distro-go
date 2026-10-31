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

- [ ] DG-06 — Allowlist por serial en la maquina de desarrollo (M1).
  - Hay dos discos USB. Disk 2 tiene datos del usuario. Filtrar solo por BusType es
    insuficiente.
  - Cierre: el guard exige numero, serial y tamano coincidentes, y rechaza Disk 2 con
    un mensaje explicito.

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

- [ ] DG-10 — Falta .gitattributes (M0).
  - Repo en Windows con codigo Go. Conviene fijar finales de linea y marcar binarios.
  - Cierre: `.gitattributes` versionado y `git ls-files --eol` sin avisos.

- [ ] DG-11 — `distrogo list` sin elevacion (M1).
  - Debe funcionar con IOCTLs de solo consulta.
  - Cierre: ejecutado desde una consola sin privilegios y lista los cuatro discos.

- [ ] DG-12 — Debian 12 live como segunda fixture (M4).
  - Layout live/ en vez de casper/ e initrd.gz en vez de initrd.lz.
  - Cierre: la deteccion de distribucion reconoce el ISO de Debian y no confunde rutas.

- [ ] DG-13 — CI para los paquetes puros (post-M2).
  - pkg/blockdev, pkg/gpt y pkg/safety no dependen de Win32 y pueden correr en Linux.
  - Cierre: workflow que ejecuta `go test ./pkg/...` con CGO deshabilitado.

## Cerrados

- [X] Viabilidad de escribir ext4 desde Windows.
  - Evidencia: e2fsck 1.47.4 con cinco pasadas sin errores, 12 binarios setuid, 1,6 GiB
    y label correcto. Detalle en docs/library-findings.md.
- [X] Retirar el validador casero del spike.
  - Evidencia: era un oraculo autorreferencial (go-diskfs leyendo lo que go-diskfs
    escribio). Eliminado; e2fsck queda como unico oraculo.
