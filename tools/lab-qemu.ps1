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

.EXAMPLE
    .\tools\lab-qemu.ps1

.EXAMPLE
    .\tools\lab-qemu.ps1 -Network -Image out\test.img

.EXAMPLE
    .\tools\lab-qemu.ps1 -Uefi -Image out\test.img
#>
[CmdletBinding()]
param(
    [string]$Image = "out/test.img",
    [string]$Iso = "testdata/isos/alpine-virt-3.24.3-x86_64.iso",
    [int]$MemoryMiB = 2048,
    [int]$Cpus = 2,
    [switch]$Network,
    [switch]$Uefi,
    [switch]$Headless
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
if (-not (Test-Path $Iso)) {
    throw "No existe el ISO '$Iso'. Comprueba testdata/isos/."
}

$qemu = Resolve-Qemu
$qemuArgs = @(
    "-m", "$MemoryMiB",
    "-smp", "$Cpus",
    "-drive", "file=$Iso,media=cdrom",
    "-drive", "file=$Image,format=raw,if=virtio"
)

if ($Network) { $qemuArgs += @("-nic", "user,model=e1000") }

if ($Uefi) {
    $ovmf = Resolve-Ovmf $qemu
    $qemuArgs += @(
        "-drive", "if=pflash,format=raw,readonly=on,file=$($ovmf.Code)",
        "-drive", "if=pflash,format=raw,file=$($ovmf.Vars)"
    )
} else {
    $qemuArgs += @("-boot", "order=d")
}

if ($Headless) {
    $qemuArgs += @("-display", "none", "-serial", "mon:stdio")
}

Write-Host ""
Write-Host "QEMU : $qemu"
Write-Host "ISO  : $Iso"
Write-Host "disco: $Image"
Write-Host "modo : $(if ($Uefi) { 'UEFI (OVMF)' } else { 'BIOS (SeaBIOS)' })$(if ($Network) { ' + red NAT' })"
Write-Host ""

Write-Host @'
Invitado: Alpine live. Todo corre en RAM, asi que cada arranque empieza limpio.
Login: root, sin contrasena.

Validacion SIN red:
  modprobe vfat
  mkdir -p /mnt/esp
  mount -t vfat /dev/vda1 /mnt/esp
  ls -la /mnt/esp
  umount /mnt/esp
  sfdisk -l /dev/vda

Si hacen falta paquetes que no estan en el CD (dosfstools, mtools):
  ip link set eth0 up
  udhcpc -i eth0
  echo "https://dl-cdn.alpinelinux.org/alpine/v3.24/main" >> /etc/apk/repositories
  echo "https://dl-cdn.alpinelinux.org/alpine/v3.24/community" >> /etc/apk/repositories
  apk update
  apk add --no-cache dosfstools mtools
  fsck.fat -n -v /dev/vda1

Apagar con: poweroff
'@

& $qemu @qemuArgs
exit $LASTEXITCODE
