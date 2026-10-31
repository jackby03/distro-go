# Hallazgos del spike H-1 (jul 2026)

El spike se ha retirado del árbol. Este documento es lo único que sobrevive de él,
junto con la salida de `e2fsck` que validó el resultado.

## Decisión que tomó el spike

**GO**: `go-diskfs` (v1.9.4) produce ext4 estructuralmente válido desde Windows.

```
e2fsck 1.47.4 · 5 pasadas sin errores (Alpine 3.24 en QEMU, imagen de 8 GiB)
distrogo-root: 19986/40448 files (0.2% non-contiguous), 431528/2097152 blocks
12 binarios setuid presentes (sudo, su, passwd, mount, pkexec...)
du -sh = 1,6 GiB · label y UUID legibles
```

Consecuencia: H4 deja de ser "escribir un writer ext4" (~3 semanas) y pasa a ser
"adaptar go-diskfs + 2 parches" (~1 semana).

## Números de referencia (Mint 22.3 Xfce, xz, bloque 128 KiB)

| Métrica | Valor |
|---|---|
| Entradas del rootfs | 507.623 (322.477 ficheros, **162.239 symlinks**, 22.899 dirs, 8 device) |
| Contenido | 6,58 GiB; mayor fichero `usr/lib/thunderbird/libxul.so` (165 MiB) |
| uid / gid distintos | 18 / 29 · setuid / setgid: 18 / 18 |
| Hardlinks | 17 inodos (38 entradas) |
| xattrs | 0 (el ISO no los lleva: `flags=0x00C0`, sin bit 0x200) |
| Walk de metadatos | 103 s (≈200 µs/entrada) |
| Instalación completa estimada | **10-15 min** (24 MiB/s, cuello = xz single-thread) |

## Bugs de `github.com/CalebQ42/squashfs` v1.4.1 (MPL-2.0)

### 1. Deadlock y pérdida de datos en `FullReader.WriteTo`
`low/data/fullreader.go` borra el bloque equivocado tras escribir:

```go
i++
delete(results, i)   // borra results[i+1], no el bloque recién escrito
```

Con resultados fuera de orden (siempre, con `runtime.NumCPU()` workers) el
consumidor espera un bloque que ya se descartó → `all goroutines are asleep - deadlock!`.
Síntoma observado: el proceso muere a los ~5 ficheros de una ISO real.
**Solución**: no usar `WriteTo`; leer con `File.Read` (secuencial, correcto) o
reimplementar el pipeline en paralelo sobre `FullReader.Block(i)`.
Coste: se pierde la descompresión multihilo de la librería.

### 2. Rutas anidadas rotas en Windows
`fs.go` usa `filepath.Clean`, que en Windows convierte `/` en `\`; después hace
`strings.Split(name, "/")` → cualquier ruta con más de un componente devuelve
`fs.ErrNotExist`.
**Solución**: navegar componente a componente (`FS.OpenFile("usr")` → `File.FS()` →
`OpenFile("bin")`) en un adapter. Es lo que debe hacer `pkg/linuxiso`.

### 3. Ficheros de 0 bytes ilegibles
`data.NewReader` hace `Block(0)`; con `BlockNum() == 0` devuelve
`invalid block index`. Afectaba a 39 ficheros del rootfs.
**Solución**: si `Size == 0`, crear el fichero vacío y no leer nada.

## Huecos de `go-diskfs` ext4 (v1.9.4)

- `Mknod` → `filesystem.ErrNotImplemented`. Afecta a 8 device nodes del rootfs.
  Irrelevante en la práctica: `/dev` es tmpfs y systemd lo crea al arrancar.
- `Link` (hardlinks) → `filesystem.ErrNotImplemented`. Afecta a 21 entradas;
  duplicar el contenido es válido para ext4 y e2fsck no se queja.
- Ambos son parcheables (~60 líneas) si algún día molestan.

## Reutilizado por los hitos siguientes

- `pkg/rootfs` (contrato `Source`/`Target`) salió de aquí y es código de producto.
- El adapter squashfs → `rootfs.Source` se reescribirá limpio en `pkg/linuxiso` (H3).
