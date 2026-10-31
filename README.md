# DistroGo

> Native, user-space installer for deploying persistent, bare-metal Linux installations to external storage directly from Windows without virtualization or live-media booting.

DistroGo is an open-source systems utility that bridges the gap between Windows and Linux deployments. Unlike traditional USB boot-media creators that write temporary Live-CD environments (requiring a subsequent boot and manual installation process), DistroGo performs a complete, native Linux installation directly onto target external disks (USB HDDs, external SSDs, flash drives) from an active Windows session.

It requires no Windows Subsystem for Linux (WSL), no Hyper-V/VirtualBox installations, and no unsigned third-party kernel drivers.

---

## Project Status

Working proof of concept. The full pipeline (GPT, FAT32 ESP, ext4, SquashFS streaming,
boot configuration, user account) is validated end to end against flat disk images and
boots in QEMU with OVMF. Writing to physical disks works and is byte-verified, but
booting the result on real hardware still fails on Secure Boot machines.

- [X] M0 — repository skeleton and `pkg/rootfs` contract
- [X] M1 — `distrogo list`, read-only disk inventory with protection verdicts
- [X] M2 — GPT partitioning of an image (verified with `sfdisk --verify`)
- [X] M3 — FAT32 ESP, shim + GRUB and a booting live session from the image
- [X] M4 — SquashFS reader, 507,623 entries streamed without full extraction
- [X] M5 — ext4 writer, `e2fsck` clean on a full 7 GB rootfs
- [X] M6 — installed system boots in QEMU: GRUB, kernel, initramfs, ext4 root, desktop
- [X] M6.5 — user account creation, own group and home ownership
- [ ] M7 — boot an installed physical disk on real hardware
- [ ] M7.5 — use the whole disk (grow the partition and the filesystem)
- [ ] M8 — dump performance
- [ ] M9 — declarative partition profile
- [ ] M10 — GUI on top of that profile
- [ ] M11 — 4K logical sector disks

Measured on Linux Mint 22.3 Xfce: 507,615 entries and 7,070,158,975 bytes written in
45 minutes, and the image boots to the greeter with no live session.

Measured on a USB 3.0 HDD: flashing an 8 GiB image takes 1m15s at 109 MB/s, and the
read-back verification matches byte for byte.

Known limitations at this stage:

- **Secure Boot blocks the bootloader.** On a machine with Secure Boot enabled the
  firmware rejects the boot chain. Disabling Secure Boot works around it; a proper fix
  needs the whole chain signed (see `docs/backlog.md`).
- **The rest of the disk is unused.** The image is built at a fixed size, so a 465 GiB
  disk flashed with an 8 GiB image keeps 7.5 GiB for the root. Growing the partition and
  the filesystem is M7.5.
- **Antivirus must allow the tool.** Defender blocks `FSCTL_LOCK_VOLUME` for unknown
  binaries. Add a process exclusion for `distrogo.exe`. The stable version needs code
  signing.
- **Writing the filesystem in place is far too slow on spinning disks.** 0.6 entries/s
  on a USB HDD. Build the image first and flash it sequentially: that is what `flash`
  is for.
- **Account creation is not idempotent.** Installing over an existing tree is not
  supported; use a fresh image per install.

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
| **Image build throughput** | About 187 entries/s regardless of the target: 7 minutes for Debian Live standard, 45 minutes for Linux Mint 22.3 Xfce |
| **Flash throughput** | 109 MB/s on a USB 3.0 HDD (8 GiB in 75 s, byte-verified); an external SSD reaches 300-500 MB/s |
| **Recommended media** | SATA or NVMe SSD in a USB 3.0 enclosure with UASP. A USB HDD works, but only for `flash` |
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

> **Warning:** commands that touch a physical disk need an elevated terminal (Run as
> Administrator) and a process exclusion in your antivirus for `distrogo.exe`. Without
> it, Windows blocks the exclusive lock on the volume.

### List Disks and Inspect Protection Flags

```powershell
distrogo.exe list

```

### Build an Installation Image

One command partitions, streams the rootfs, seeds the boot configuration and creates the
user account. This is where the time goes: about 7 minutes for Debian Live standard and
45 minutes for Linux Mint.

```powershell
distrogo.exe partition --image out\debian.img --size 8GiB `
  --boot-from testdata\isos\debian-live-13.7.0-amd64-standard.iso `
  --install --user demo

```

### Flash the Image to a Physical Disk

Sequential write, which is what makes a spinning disk usable: 8 GiB in 75 seconds on a
USB 3.0 HDD, with an optional byte-for-byte read-back.

```powershell
distrogo.exe flash --image out\debian.img --target-disk 3 `
  --expect-serial <serial de distrogo list> --verify

```

### Inspect a Physical Disk Without Writing

Prints what would be destroyed, opens the disk, reads its partition table and closes it.
Nothing is written. `-probe` additionally reports which volume control IOCTL your
antivirus is blocking.

```powershell
distrogo.exe install --target-disk 3 --expect-serial <serial>
distrogo.exe install --target-disk 3 --expect-serial <serial> -probe

```

### Re-seed the Boot Configuration

Iterating on the bootloader takes about a second, because the rootfs is left alone:

```powershell
distrogo.exe boot --image out\test.img --title "Mint 22.3 instalado"

```

### Boot the Result in QEMU

```powershell
.\tools\lab-qemu.ps1 -Uefi -NoGuestIso -Image out\test.img

```

`install --apply` writes the filesystem in place on a physical disk. It works, but on a
spinning disk it is far too slow: use `flash` instead.

---

## Safety & Security Protocols

To avoid data loss on production machines:

* Disks hosting the active Windows system volume (`C:\`) or active bootloaders are strictly locked and cannot be selected as target drives.
* Operations fail closed unless the target interface explicitly reports a removable/USB bus topology, or an explicit `--allow-non-usb` override flag is passed.
* All sector calculations and layout operations support dry-run analysis prior to execution.

---

## License

This project is licensed under the MIT License. See the [LICENSE](https://www.google.com/search?q=LICENSE) file for complete details.