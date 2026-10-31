# DistroGo

> Native, user-space installer for deploying persistent, bare-metal Linux installations to external storage directly from Windows without virtualization or live-media booting.

DistroGo is an open-source systems utility that bridges the gap between Windows and Linux deployments. Unlike traditional USB boot-media creators that write temporary Live-CD environments (requiring a subsequent boot and manual installation process), DistroGo performs a complete, native Linux installation directly onto target external disks (USB HDDs, external SSDs, flash drives) from an active Windows session.

It requires no Windows Subsystem for Linux (WSL), no Hyper-V/VirtualBox installations, and no unsigned third-party kernel drivers.

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
| **Supported Distributions** | Debian, Linux Mint, Ubuntu, and standard live-boot compatible derivatives |
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

### Install Linux Directly to Target Disk

```powershell
distrogo.exe install `
  --iso "C:\Path\To\linuxmint-22.3-xfce-64bit.iso" `
  --target-disk 3 `
  --hostname "portable-mint"

```

---

## Safety & Security Protocols

To avoid data loss on production machines:

* Disks hosting the active Windows system volume (`C:\`) or active bootloaders are strictly locked and cannot be selected as target drives.
* Operations fail closed unless the target interface explicitly reports a removable/USB bus topology, or an explicit `--allow-non-usb` override flag is passed.
* All sector calculations and layout operations support dry-run analysis prior to execution.

---

## License

This project is licensed under the MIT License. See the [LICENSE](https://www.google.com/search?q=LICENSE) file for complete details.