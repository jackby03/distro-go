# DistroGo

> Native, user-space installer for deploying persistent, bare-metal Linux installations to external storage directly from Windows without virtualization or live-media booting.

DistroGo is an open-source systems utility that bridges the gap between Windows and Linux deployments. Unlike traditional USB boot-media creators that write temporary Live-CD environments (requiring a subsequent boot and manual installation process), DistroGo performs a complete, native Linux installation directly onto target external disks (USB HDDs, external SSDs, flash drives) from an active Windows session.

It requires no Windows Subsystem for Linux (WSL), no Hyper-V/VirtualBox installations, and no unsigned third-party kernel drivers.

---

## Project Status

Proof of concept with a complete pipeline. Everything is validated against flat disk
images (`.img`) booted in QEMU with OVMF; nothing is written to physical hardware yet.

- [X] M0 — repository skeleton and `pkg/rootfs` contract
- [X] M1 — `distrogo list`, read-only disk inventory with protection verdicts
- [X] M2 — GPT partitioning of an image (verified with `sfdisk --verify`)
- [X] M3 — FAT32 ESP, shim + GRUB and a booting live session from the image
- [X] M4 — SquashFS reader, 507,623 entries streamed without full extraction
- [X] M5 — ext4 writer, `e2fsck` clean on a full 7 GB rootfs
- [X] M6 — installed system boots: GRUB, kernel, initramfs, ext4 root, desktop
- [ ] M6.5 — user account creation during install
- [ ] M7 — first writes to `\\.\PhysicalDrive`
- [ ] M8 — dump performance

Measured end to end on Linux Mint 22.3 Xfce: 507,615 entries and 7,070,158,975 bytes
written in 45 minutes, and the resulting image boots to the greeter with no live
session and no installer icon.

Known limitations at this stage:

- No user account is created yet. The ISO ships only `root`, locked; `casper`
  creates the live user at boot time, so an installed tree has no account to log in
  with.
- The image can only be re-read by DistroGo while no other tool has written to it.
  The ext4 reader validates inode checksums that the filesystem does not advertise,
  so `e2fsck` or a writable mount invalidates them for us (not for Linux).
- The dump runs at about 187 entries per second, dominated by per-entry overhead
  rather than bandwidth.

---

## Key Features

* **True Bare-Metal Deployment:** Installs full, persistent Linux root filesystems (`ext4`) with native UEFI bootloaders, ready to boot directly on any physical x86_64 hardware.
* **Pure Windows User-Space Execution:** Performs low-level disk partitioning, filesystem creation, and stream decompression entirely via Win32 storage APIs and user-space tooling.
* **Direct SquashFS-to-ext4 Streaming:** Streams rootfs archives directly from standard distribution ISOs (Linux Mint, Ubuntu, Debian) without temporary extraction to NTFS, preserving POSIX file permissions, UIDs/GIDs, symlinks, and setuid/sticky bits.
* **System Disk Protection:** Hardcoded guardrails inspect volume mount points and bus types, preventing accidental writes to internal Windows OS drives or BitLocker partitions.
* **Portable UEFI Generation:** Automatically provisions an EFI System Partition (ESP) and writes universal fallback boot entries (`\EFI\BOOT\BOOTX64.EFI`) alongside tailored `grub.cfg` and `/etc/fstab` mappings.

---

## Architecture Overview

DistroGo operates through a modular pipeline designed to minimize dependencies and eliminate unnecessary disk roundtrips:

```text
[ Linux ISO (.iso) ]
        │
        ├── 1. Win32 Storage API
        │      └── Lock volumes, initialize GPT, create ESP (FAT32) & Root (ext4)
        │
        ├── 2. User-Space Filesystem Engine
        │      └── Format ext4 partition and capture superblock UUID
        │
        ├── 3. In-Memory SquashFS Streamer
        │      └── Extract /casper/filesystem.squashfs directly to ext4 blocks
        │
        └── 4. Bootloader Injection
               └── Install portable GRUB EFI binary and link root UUID in /etc/fstab

```

---

## Technical Specifications

| Parameter | Specification |
| --- | --- |
| **Target OS (Host)** | Windows 10 / 11 (x86_64) |
| **Target Storage** | USB External Hard Drives, External NVMe/SATA SSDs, High-Speed Flash Drives |
| **Partition Scheme** | GPT (GUID Partition Table) |
| **Partition 1 (ESP)** | 512 MiB, FAT32 (`c12a7328-f81f-11d2-ba4b-00a0c93ec93b`) |
| **Partition 2 (Root)** | Remaining Capacity, ext4 (`0fc63daf-8483-4772-8e79-3d69d8477de4`) |
| **Supported Distributions** | Debian (`live/`), Linux Mint and Ubuntu (`casper/`), and live-boot compatible derivatives |
| **Measured Throughput** | 507,615 entries and 7.07 GB in 45 minutes on Linux Mint 22.3 Xfce |
| **Build Flags** | Pure static Go compilation (`CGO_ENABLED=0`, `GOOS=windows`, `GOARCH=amd64`) |

---

## Development & Test Harness

DistroGo isolates testing routines from physical hardware by utilizing raw disk images (`.img`) evaluated inside QEMU with Alpine Linux verification guests:

### 1. Build the Binary

```powershell
go build -ldflags="-s -w" -o bin/distrogo.exe ./cmd/distrogo

```

### 2. Run Test Suite

```powershell
go test -v -race ./pkg/...

```

### 3. Verify Image Integrity via QEMU (No Hardware Required)

```powershell
qemu-system-x86_64 -m 2G -smp 2 -nographic -serial mon:stdio `
  -drive file=alpine-virt-3.24.3-x86_64.iso,media=cdrom `
  -drive file=test.img,format=raw,if=virtio

```

Inside the Alpine guest environment:

```sh
apk add e2fsprogs dosfstools
e2fsck -fn /dev/vdb2
debugfs -R 'ls -l /' /dev/vdb2
fsck.fat -n /dev/vdb1

```

---

## CLI Usage (PoC)

> **Warning:** Executing storage modification commands requires an elevated terminal session (Run as Administrator).

### List Disks and Inspect Protection Flags

```powershell
distrogo.exe list

```

### Install a Distribution into an Image (end to end)

One command formats the disk, streams the rootfs and seeds the boot configuration:

```powershell
distrogo.exe partition --image out\test.img --size 12GiB --boot-from testdata\isos\linuxmint-22.3-xfce-64bit.iso --install --title "Mint 22.3 instalado"

```

### Re-seed the Boot Configuration

Iterating on the bootloader takes about a second, because the rootfs is left alone:

```powershell
distrogo.exe boot --image out\test.img --title "Mint 22.3 instalado"

```

### Boot the Result

```powershell
.\tools\lab-qemu.ps1 -Uefi -NoGuestIso -Image out\test.img

```

`distrogo.exe install --target-disk`, which writes to a physical disk, does not exist
yet. It is M7.

---

## Safety & Security Protocols

To avoid data loss on production machines:

* Disks hosting the active Windows system volume (`C:\`) or active bootloaders are strictly locked and cannot be selected as target drives.
* Operations fail closed unless the target interface explicitly reports a removable/USB bus topology, or an explicit `--allow-non-usb` override flag is passed.
* All sector calculations and layout operations support dry-run analysis prior to execution.

---

## License

This project is licensed under the MIT License. See the [LICENSE](https://www.google.com/search?q=LICENSE) file for complete details.