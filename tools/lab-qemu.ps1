<#
.SYNOPSIS
    Arranca la VM de laboratorio con la imagen bajo prueba.

.DESCRIPTION
    Lanza QEMU con el ISO de Alpine como invitado y la imagen de disco como
    segundo dispositivo. Imprime en pantalla la receta del invitado para no
    tener que recordarla, y deja la red desactivada salvo que se pida.

    Rutas relativas se resuelven desde la raiz del repositorio.

.PARAMETER Image
    Imagen de disco a adjuntar. Por defecto out/test.img.

.PARAMETER Iso
    ISO del invitado. Por defecto el Alpine del banco de pruebas.

.PARAMETER MemoryMiB
    RAM del invitado en MiB. Por defecto 2048.

.PARAMETER Cpus
    Numero de CPUs. Por defecto 2.

.PARAMETER Network
    Anade tarjeta de red con NAT. Hace falta solo para instalar paquetes que no
    vienen en el CD de Alpine (dosfstools, mtools). El invitado necesita ademas
    `ip link set eth0 up` y `udhcpc -i eth0`.

.PARAMETER Uefi
    Arranca con OVMF en lugar de BIOS. Usa las variables de out/OVMF_VARS.fd, que
    se crean a partir de la plantilla de QEMU la primera vez. Necesario para M3.4
    y en adelante.

.PARAMETER Headless
    Sin ventana: consola serie en el terminal. Util para ejecuciones desatendidas.

.PARAMETER NoGuestIso
    No conecta el ISO del invitado. Es lo que hace falta para arrancar el disco
    bajo prueba: con un CD conectado, el firmware arranca primero el medio
    extraible y el sistema de la imagen nunca se llega a probar.

.PARAMETER ResetNvram
    Borra out/OVMF_VARS.fd antes de arrancar, para que OVMF vuelva a su orden de
    arranque de fabrica. Util cuando la NVRAM conserva una entrada vieja al CD.

.EXAMPLE
    .\tools\lab-qemu.ps1

.EXAMPLE
    .\tools\lab-qemu.ps1 -Network -Image out\test.img

.EXAMPLE
    .\tools\lab-qemu.ps1 -Uefi -NoGuestIso -Image out\live.img
#>
[CmdletBinding()]
param(
    [string]$Image = "out/test.img",
    [string]$Iso = "testdata/isos/alpine-virt-3.24.3-x86_64.iso",
    [int]$MemoryMiB = 2048,
    [int]$Cpus = 2,
    [switch]$Network,
    [switch]$Uefi,
    [switch]$Headless,
    [switch]$NoGuestIso,
    [switch]$ResetNvram
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $PSScriptRoot)

function Resolve-Qemu {
    $cmd = Get-Command qemu-system-x86_64 -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    $fallback = "C:\Program Files\qemu\qemu-system-x86_64.exe"
    if (Test-Path $fallback) { return $fallback }

    throw "No encuentro qemu-system-x86_64. Anade QEMU al PATH o instala en C:\Program Files\qemu."
}

function Resolve-Ovmf {
    param([string]$QemuExe)

    $share = Join-Path (Split-Path -Parent $QemuExe) "share"
    $code = Join-Path $share "edk2-x86_64-code.fd"
    $template = Join-Path $share "edk2-i386-vars.fd"

    if (-not (Test-Path $code)) { throw "Falta el firmware OVMF en $share" }
    if (-not (Test-Path $template)) { throw "Falta la plantilla de NVRAM en $share" }

    # La NVRAM se escribe, asi que se trabaja sobre una copia.
    $vars = "out/OVMF_VARS.fd"
    if (-not (Test-Path $vars)) {
        New-Item -ItemType Directory -Force out | Out-Null
        Copy-Item $template $vars
    }
    return @{ Code = $code; Vars = $vars }
}

if (-not (Test-Path $Image)) {
    throw "No existe la imagen '$Image'. Generala con: .\out\distrogo.exe partition --image $Image --size 8GiB"
}
if (-not $NoGuestIso -and -not (Test-Path $Iso)) {
    throw "No existe el ISO '$Iso'. Comprueba testdata/isos/."
}

$qemu = Resolve-Qemu
$qemuArgs = @(
    "-m", "$MemoryMiB",
    "-smp", "$Cpus"
)

if (-not $NoGuestIso) {
    $qemuArgs += @("-drive", "file=$Iso,media=cdrom")
}
$qemuArgs += @("-drive", "file=$Image,format=raw,if=virtio")

if ($Network) { $qemuArgs += @("-nic", "user,model=e1000") }

if ($Uefi) {
    if ($ResetNvram -and (Test-Path "out/OVMF_VARS.fd")) {
        Remove-Item "out/OVMF_VARS.fd" -Force
    }
    $ovmf = Resolve-Ovmf $qemu
    $qemuArgs += @(
        "-drive", "if=pflash,format=raw,readonly=on,file=$($ovmf.Code)",
        "-drive", "if=pflash,format=raw,file=$($ovmf.Vars)"
    )
} elseif (-not $NoGuestIso) {
    $qemuArgs += @("-boot", "order=d")
}

if ($Headless) {
    $qemuArgs += @("-display", "none", "-serial", "mon:stdio")
}

Write-Host ""
Write-Host "QEMU : $qemu"
if ($NoGuestIso) {
    Write-Host "ISO  : (ninguno: arranca el disco)"
} else {
    Write-Host "ISO  : $Iso"
}
Write-Host "disco: $Image"
Write-Host "modo : $(if ($Uefi) { 'UEFI (OVMF)' } else { 'BIOS (SeaBIOS)' })$(if ($Network) { ' + red NAT' })"
Write-Host ""

Write-Host @'
Invitado: Alpine live. Todo corre en RAM, asi que cada arranque empieza limpio.
Login: root, sin contrasena.

Con -NoGuestIso no hay invitado: el firmware arranca lo que haya en la imagen.
Si el disco no arranca, prueba -ResetNvram para devolver OVMF a su orden de fabrica.

Antes de regenerar la imagen, apaga la VM: QEMU mantiene el fichero abierto y el
invitado seguiria viendo la version anterior.

Herramientas, sin red (e2fsprogs y sfdisk vienen en el propio CD):
  apk add --no-cache e2fsprogs sfdisk

Herramientas que NO estan en el CD (dosfstools, mtools) y necesitan el repositorio:
  ip link set eth0 up
  udhcpc -i eth0
  echo "https://dl-cdn.alpinelinux.org/alpine/v3.24/main" >> /etc/apk/repositories
  echo "https://dl-cdn.alpinelinux.org/alpine/v3.24/community" >> /etc/apk/repositories
  apk update
  apk add --no-cache e2fsprogs dosfstools mtools

Comprobar la tabla de particiones:
  sfdisk -l /dev/vda
  sfdisk --verify /dev/vda

Comprobar el root ext4:
  e2fsck -fn /dev/vda2
  mount -t ext4 -o ro /dev/vda2 /mnt
  ls -ln /mnt | head
  du -sh /mnt
  find /mnt -perm -4000 | wc -l
  umount /mnt

Comprobar la ESP FAT32:
  fsck.fat -n -v /dev/vda1
  mdir -i /dev/vda1 ::/
  mdir -i /dev/vda1 ::/EFI/boot
  mount -t vfat /dev/vda1 /mnt && ls -la /mnt && umount /mnt

Apagar con: poweroff
'@

& $qemu @qemuArgs
exit $LASTEXITCODE
