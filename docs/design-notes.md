# Notas de diseño — direcciones que se exploraron

Documento de ideas, no de trabajo hecho. Nada de lo que hay aquí está implementado: son
las salidas que se identificaron cuando M7 chocó contra sus tres muros, con las cuentas y
los requisitos que se conocen hasta hoy. Se guarda para no volver a empezar de cero cada
vez que el tema reaparezca.

## 1. El problema que dejó M7

Tres muros, y solo el primero es nuestro:

1. **Secure Boot** rechaza la cadena de arranque en un portátil real (`DG-34`).
2. **Velocidad**: construir el sistema de ficheros sobre el disco dio 0,6 entradas/s en un
   HDD por USB. Resuelto para el medio con `flash` (109 MB/s), pero sigue costando 45
   minutos construir la imagen de Mint (`DG-33`).
3. **Fricción de Windows**: Defender bloqueando los IOCTL de volumen, elevación, bloqueo
   de volúmenes (`DG-32`).

Los tres desaparecen si la instalación la hace el kernel de Linux en el equipo destino en
lugar de un programa de Windows sobre un disco crudo.

## 2. Instalación desatendida (preseed / autoinstall)

Un medio de arranque con las respuestas ya puestas: reparticiona, formatea, copia el
sistema, instala el gestor de arranque y reinicia. Sin una sola pregunta.

Lo que resuelve:

- **Secure Boot**: el medio lleva el `shim` y el núcleo firmados de la distro. El firmware
  los acepta sin tocar la BIOS.
- **Velocidad**: escribe el kernel con asignación diferida. Minutos, no horas.
- **Fricción**: no hay Windows corriendo, así que no hay candados ni antivirus que valga.

Lo que **no** resuelve: sigue haciendo falta arrancar desde otro medio, así que el
producto pasa de "un dispositivo" a "dos". El disco destino no necesita preparación de
ninguna clase: da igual cómo esté, se va a borrar entero.

Trampa conocida: el formato del fichero de respuestas es distinto en cada distro. Debian
(`preseed`) y Ubuntu (`autoinstall`) están bien documentados; **Mint sigue con ubiquity y
su soporte es flojo**. Primer objetivo razonable: Debian.

Punto delicado que hay que trasladar al fichero: elegir el disco **por serial**, no por
número. Es la misma precaución de `--expect-serial`, escrita en el idioma del instalador.

## 3. Autoinstalación en un solo disco

Para recuperar la promesa de "un solo USB": que el medio y el destino sean el mismo disco.

El orden de las particiones no es libre, porque una partición solo crece hacia la derecha:

```
[ sda1 ESP 512M ][ sda2 root ............ ][ sda3 live N GB ]
                                         ↑
                          se elimina al final y root crece aquí
```

- El live va **al final**. Si va en medio, el root no puede crecer sin mover datos.
- La ESP se queda quieta, con su GRUB y su `grub.cfg` apuntando al UUID del root nuevo.

Requisito si se borra el live **durante** la instalación: arrancar con `toram`, porque un
live normal lee el squashfs del medio a demanda y al borrar esa partición se cae a mitad.
`toram` exige RAM para el rootfs (1,2 GB Debian, ~3 GB Mint). Esta condición desaparece
con la variante del punto 4.

Es un script de shell de unas cien líneas, no un proyecto: `sgdisk` para el esquema,
`mkfs`, `unsquashfs`, `grub-install`, y al final borrar y estirar.

## 4. Preguntar tras el primer inicio (preferido sobre borrar durante la instalación)

En vez de liberar el volumen live dentro del instalador, hacerlo cuando el usuario ya está
en el sistema instalado:

1. El primer arranque deja una marca en `/var/lib/distrogo/`.
2. Un autostart pregunta una sola vez: liberar los GB del instalador o dejarlos.
3. Si acepta: borrar la partición live, estirar el root y crecer el sistema de ficheros.

Ventajas sobre la variante anterior: **no hace falta `toram`**, no hay paso destructivo
durante la instalación, y la decisión la toma el usuario con un sistema ya funcionando. Si
algo falla, el disco sigue arrancando.

Los pasos, en caliente y sin reiniciar:

```
sgdisk -d <live> /dev/sdX     # fuera la particion del instalador
growpart /dev/sdX <root>      # estira la entrada del root
resize2fs /dev/sdX<root>      # el sistema de ficheros ocupa lo nuevo
```

**ext4 crece montado y en uso**; es reducirlo lo que exige desmontar. Es lo que hacen las
imágenes de nube y Raspberry Pi en el primer arranque.

Dos pegas y su salida:

- `gdisk` y `cloud-guest-utils` no están garantizados en toda distro. Aquí se puede
  reutilizar `pkg/gpt`: un binario propio, `CGO_ENABLED=0`, que borre la entrada del live
  y estire la del root reescribiendo las cabeceras. Solo quedaría depender de `resize2fs`.
- Pendiente de verificar con los ojos: que `resize2fs` agrande en caliente en esa distro,
  con ese kernel y esa versión de e2fsprogs. Diez minutos en la VM: tres comandos a mano y
  `df -h /` antes y después.

## 5. Aprovisionamiento por red (PXE)

La forma de tener **un solo dispositivo** sin USB: el equipo arranca el instalador desde la
red y se instala en su propio disco, que es lo único que hay.

Piezas: DHCP con `next-server` y `bootfile` (o proxyDHCP en tu máquina), servidor TFTP o
HTTP con kernel e initrd, y el rootfs por HTTP o NFS.

Herramientas que ya lo hacen, para no reinventarlas: **iVentoy** (ISOs por red),
**netboot.xyz**, **MAAS**, **Foreman**, **FOG** y **Clonezilla SE** con **udpcast** para
desplegar la misma imagen en muchas máquinas por multicast.

Secure Boot no estorba: el shim de Debian está firmado.

Cuentas de red, que conviene tener claras porque Ethernet **no es más rápido que USB 3.0**:

```
1 GbE      ~110 MB/s    (lo mismo que el USB 3.0 del laboratorio)
2.5 GbE    ~280 MB/s
10 GbE    ~1100 MB/s
```

La ventaja real no es la velocidad por máquina, es servir a muchas desde una lectura, y no
manipular medios. El cuello suele ser el disco del destino, no la red.

## 6. Appliances virtuales (M12)

La idea con mejor relación esfuerzo/resultado: la imagen que ya construimos **es** un
appliance. Solo hay que meterla en el envase de cada hipervisor.

Verificado en el equipo de desarrollo con `qemu-img 11.1.0`, que viene con el QEMU que ya
usamos:

```
raw 64 MiB de ceros  →  qcow2 197 KB · vmdk 65 KB · vhd 2 KB · vhdx 8 MB · vdi 1 KB
```

- QEMU/libvirt → `qcow2`
- VMware → `vmdk`, o el paquete `ova` (descriptor `.ovf` + manifiesto `.mf` + TAR)
- VirtualBox → `vdi` y un `.vbox`
- Hyper-V → `vhd`/`vhdx`, generación 2 con UEFI

Para OVA el VMDK tiene que ir con `subformat=streamOptimized`.

Pegas honestas: el disco sigue siendo del tamaño de la imagen (M7.5); construir el
appliance cuesta lo mismo que construir la imagen (M8), aunque se paga una vez y se arranca
cien; Hyper-V trae Secure Boot activado por defecto en Gen2 y habría que desactivarlo; y los
guest tools (agente de QEMU, Guest Additions, VMware Tools) requieren red en el invitado.

## 7. Laboratorios de pentesting (idea aparte, no de este proyecto)

Un "Packet Tracer" que levante dispositivos virtuales y exporte el laboratorio como fichero.
Ya existe en los dos extremos: **GNS3** y **Eve-NG** tienen editor visual pero son pesados;
**containerlab** y **Kathará** son ligeros y declarativos (un YAML) pero no tienen editor.

El hueco real: el editor visual sobre un esquema declarativo. Y el valor educativo de
verdad no es el dibujo: es el **guion con verificación automática** de objetivos.

Trampas: las imágenes propietarias (IOS de Cisco) no se pueden redistribuir, y un fichero
que referencia `metasploitable2` sin versión deja de funcionar en un año.

## 8. Qué se reutiliza y qué se tira

Se reutiliza: lectura de ISO, extracción de kernel e initrd, lector de SquashFS, el
conocimiento de ESP y GRUB, `flash` para escribir el medio, `pkg/gpt`, y el arnés de QEMU.

Se tira: el escritor de ext4 en espacio de usuario y toda la pelea con los bloqueos de
volumen de Windows. Es justo la parte que más ha costado.

## 9. Experimentos baratos, en orden

1. **Preseed de Debian en QEMU** con un disco vacío: una hora. Demuestra la idea entera.
2. **Autoinstalación en un solo disco** con `toram`: una tarde.
3. **Preguntar tras el login y crecer**: diez minutos de comprobación a mano, una tarde de
   automatización.
4. **PXE en QEMU**: una tarde, sin hardware.
5. **Appliance `qcow2` y `ova`**: uno o dos turnos, porque `qemu-img` ya hace el trabajo.

## 10. Decisiones pendientes (humanas, no técnicas)

- ¿El producto es "un Linux portátil en un USB" (un dispositivo) o "equipos instalados y
  configurados sin intervención" (dos dispositivos, o red)?
- Si se adopta el volumen live: ¿conservarlo como rescate, o liberarlo tras preguntar?
- ¿Se firma el binario y la cadena de arranque (`DG-34`), o se asume Secure Boot desactivado?
