# Notas de hardware (M7)

Lo que se aprende escribiendo en un disco de verdad y no se ve en un `.img` de QEMU.
Se anota aqui en bruto, con los mensajes de error y las medidas, para no repetir el
camino.

## Disco de pruebas

- Disk 3, `USB3.0 externo`, serial `SN-PRUEBAS-0001`, 465,8 GiB.
- Sectores: 512 logicos, 4096 fisicos. Por eso el M11 (sectores de 4K logicos) no
  bloquea este hito.
- Arranca con OVMF y arranca el sistema instalado, asi que sirve de extremo a
  extremo.

## 1. Defender bloquea el control del volumen

Medido con `install --target-disk 3 --expect-serial ... -probe`, consola elevada
(`whoami /groups | Select-String "S-1-16-12288"` confirma `High Mandatory Level`):

```
volumen \\.\E:
  abrir lectura+escritura : ok
  FSCTL_DISMOUNT_VOLUME   : ok
  FSCTL_LOCK_VOLUME       : FALLO: ioctl 0x00090018: Access is denied.
  IOCTL_VOLUME_OFFLINE    : FALLO: ioctl 0x0056C000: Access is denied.
```

Con el antivirus pausado:

```
  FSCTL_LOCK_VOLUME       : ok
```

Con Defender **activo** y una exclusion de **proceso** (`distrogo.exe` en
Seguridad de Windows, o `Add-MpPreference -ExclusionProcess`):

```
  FSCTL_LOCK_VOLUME       : ok
```

Conclusiones:

- Defender no bloquea abrir el volumen en lectura+escritura ni desmontarlo. Bloquea
  exactamente los dos IOCTL que dan control real sobre el volumen.
- Basta una exclusion de proceso. No hace falta excluir carpeta, unidad ni
  desactivar nada.
- `FSCTL_LOCK_VOLUME` devuelve `ERROR_ACCESS_DENIED` tanto por falta de permisos
  como por volumen en uso, asi que el mensaje tiene que comprobar la elevacion
  antes de acusar a nadie.
- Pendiente de producto: la version estable necesita firma de codigo. Es la unica
  forma de que Defender deje de tratar como desconocido a un binario que abre
  `\\.\PhysicalDriveN`.

## 2. Windows exige alineacion de sector en un disco crudo

Primer intento real, formateando el ext4:

```
error: formatear el root: formatear ext4: error writing GDT: error writing GDT for
block 0 to disk: escribir 238272 bytes en 537923584: The parameter is incorrect.
```

- La escritura del GDT son 238.272 bytes, que no es multiplo de 512.
- En un fichero da igual; en `\\.\PhysicalDriveN` Windows devuelve
  `ERROR_INVALID_PARAMETER` si el offset **o** el tamano no son multiplos del sector
  logico. Tienen que ser los dos.
- Solucion adoptada en `pkg/windisk`: los accesos no alineados pasan por
  lectura-modificacion-escritura de los sectores afectados. Cubre tambien la semilla
  de checksums del ext4, que son 4 bytes en un offset que no es multiplo de 512.
- Pruebas: `pkg/windisk/disk_windows_test.go`, con el caso real (238.272 bytes en
  537.923.584).

## 3. Rutas de volumen

- `\\.\E:` abre para lectura y escritura, sin problema.
- `\\?\Volume{7db8a9b9-...}\` devuelve `ERROR_PATH_NOT_FOUND` en este Windows,
  aunque sea exactamente la ruta que informa `Get-Volume`. Probado tambien desde
  PowerShell con el mismo resultado, asi que no es cosa de nuestro codigo.
- Se usa la letra de unidad, que ademas es lo que el usuario reconoce.

## 4. Construir el sistema de ficheros sobre el disco es inviable

Medido con la instalacion real de Debian Live standard (78.316 entradas) en el HDD
por USB:

```
[hh:mm:ss] 77/78316 entradas · 0/s · ...
```

77 entradas en 2 minutos: **0,6 entradas/s**. A ese ritmo serian unas 36 horas.

Comparacion con la misma instalacion sobre un `.img` en NVMe: **187 entradas/s**
(Mint, 507.615 entradas en 45 minutos).

Causas, por este orden:

1. El escritor hace decenas de accesos aleatorios por fichero: resolver la ruta,
   asignar inodo, tocar el bloque del directorio, actualizar contadores del grupo,
   escribir el contenido. Nada se agrupa.
2. Windows trata los discos extraibles en modo "extraccion rapida", que desactiva la
   cache de escritura del dispositivo. Cada escritura va a la superficie: en un HDD
   son una busqueda y una rotacion.
3. Un 2.5" por USB 3.0 da 100-120 MB/s en secuencia pero unos cientos de IOPS en
   accesos dispersos.

Decision: **construir la imagen y copiarla secuencialmente** (`distrogo flash`). La
imagen se construye donde los accesos aleatorios son baratos (SSD interno, o
memoria) y el disco solo recibe una escritura de principio a fin. De regalo:
reintentar el flasheo sin reconstruir, y grabar la misma imagen en varios discos.

## 5. GPT al copiar una imagen mas pequena que el disco

Una imagen de 8 GiB copiada a un disco de 465 GiB lleva la cabecera de respaldo del
GPT al final de la imagen, no al final del disco. Linux arranca igual, porque usa el
primario, pero `sfdisk --verify` y cualquier editor de particiones lo denuncian.

`flash` relee la tabla copiada y la recommitea con el layout del disco real. Las
particiones no se mueven: quedan 457 GiB sin particionar.


## 6. Velocidad de escritura segun bus y medio

Estimaciones de orden de magnitud para la escritura **secuencial** que hace `flash`,
que es la que manda cuando el camino es copiar una imagen. No son medidas en este
equipo salvo la fila marcada: son ordenes tipicos de bus y de disco, y sirven para
decidir que hardware recomendar, no para prometer cifras.

Los tiempos salen de los tamanos reales que manejamos: Mint 22.3 deja unos 7,5 GB
con metadatos y Debian Live standard unos 3,9 GB.

```
medio                          escr. real     Mint 7,5 GB   Debian 3,9 GB   cuello
USB 2.0  + HDD 2.5"            30-35 MB/s       4m 04s         2m 07s        bus
USB 2.0  + SSD                 30-35 MB/s       4m 04s         2m 07s        bus
USB 3.0  + HDD 2.5"            90-110 MB/s      1m 15s         0m 39s        disco
USB 3.0  + SSD SATA            300-400 MB/s     0m 21s         0m 11s        bus
USB-C G2 + SSD SATA            450-500 MB/s     0m 16s         0m 08s        disco SSD
USB-C G2 + NVMe                700-900 MB/s     0m 09s         0m 05s        bus
SATA     + HDD 3.5"            150-200 MB/s     0m 42s         0m 22s        disco
SATA     + HDD 2.5"            100-130 MB/s     1m 04s         0m 33s        disco
SATA     + SSD                  450-520 MB/s     0m 15s         0m 08s        interfaz
NVMe interno (Gen3 x4)        1500-2500 MB/s    0m 04s         0m 02s        nada
```

Lectura de la tabla:

- Con HDD manda el disco. Cambiar de USB a SATA solo gana al pasar de 2.5" a 3.5";
  el plato de un 2.5" no pasa de 110 MB/s ni con USB-C.
- Con SSD manda el bus, hasta que el bus supera al disco: un SSD SATA tope son
  ~500 MB/s, asi que USB-C Gen 2 no aporta nada frente a SATA.
- Con NVMe manda el bus en cuanto se pasa de 10 Gbps.
- USB 2.0 manda siempre: 30-35 MB/s reales y un SSD no cambia nada.

El peor escenario realista es USB 2.0 con cualquier cosa. El que estamos usando en
el laboratorio, USB 3.0 con HDD 2.5", es el segundo peor.

### Matices que las cifras no dicen

- **UASP**: muchos gabinetes USB 3.0 hablan el protocolo antiguo (BOT). Con BOT un
  SSD se queda en ~200 MB/s aunque el bus de 400. Si el dispositivo no anuncia
  UASP, ese es el techo.
- **Puentes**: los gabinetes USB-C con JMS583 o RTL9210 dan ~900 MB/s con NVMe; los
  baratos, limitados a Gen 1, se quedan en ~400.
- **Pendrives**: al agotar la cache SLC bajan a 10-20 MB/s. Un pendrive de 32 GB
  puede tardar mas que el HDD.
- **Verificar lee, no escribe**: en HDD, 90-100 MB/s sostenidos; en SSD, a la
  velocidad del bus.
- **Camino aleatorio**: nada de esta tabla aplica a `install --apply`, que hace
  decenas de accesos dispersos por fichero. Ahi manda el IOPS: un HDD por USB da
  100-200, por eso se midio 0,6 entradas/s; un SSD da 10.000-20.000 y el limite
  volveria a ser nuestro escritor.

### Medido en el laboratorio

Esto ya no es estimacion. `flash` de una imagen de 8 GiB al HDD por USB 3.0 del
banco de pruebas:

```
[18:16:19] volumen bloqueado y desmontado: \\.\E:
[18:16:19] disco 3 abierto en exclusiva, 465.8 GiB
[18:17:34] escritos 8.0 GiB en 1m15s (109 MB/s)
[18:18:49] verificado 8.0 GiB / 8.0 GiB (109 MB/s)
```

109 MB/s de escritura y otros tantos de lectura, con coincidencia byte a byte.
Clava la estimacion de la tabla para USB 3.0 con HDD 2.5", y confirma que el
camino secuencial es el bueno.

### Recomendacion de hardware

- **Recomendado**: SSD SATA o NVMe en gabinete USB 3.0 con UASP. El flasheo baja a
  ~20 s y el camino aleatorio deja de ser inviable.
- **Aceptable**: HDD por USB 3.0. Flasheo de 1-2 minutos; el camino aleatorio, no.
- **Evitar**: USB 2.0 y pendrives baratos. No es que sea lento, es que no termina.

Y conviene no perder de vista lo que de verdad pesa: **construir la imagen de Mint
son 45 minutos** mires el disco que mires, porque el cuello es el escritor y no el
medio. Optimizar el medio ahorra un minuto; optimizar el escritor (M8) ahorra media
hora.

## Verificacion en el invitado

Con el disco fisico conectado, desde QEMU (`-drive file=\\.\PhysicalDrive3`):

```
sfdisk --verify /dev/vda
e2fsck -fn /dev/vda2
fsck.fat -n /dev/vda1
```

Y el arranque real: login con la cuenta creada, `id`, `sudo -v`.

