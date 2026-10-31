package main

import (
	"io"
	"os"

	"github.com/diskfs/go-diskfs/filesystem/ext4"

	"distrogo/pkg/rootfs"
)

// ext4Target materializa el contrato rootfs.Target sobre go-diskfs/ext4.
// Es un adapter fino: no añade semántica, sólo traduce.
type ext4Target struct {
	fs *ext4.FileSystem
}

func (t *ext4Target) Mkdir(e rootfs.Entry) error { return t.fs.Mkdir(e.Path) }

func (t *ext4Target) Create(e rootfs.Entry) (io.WriteCloser, error) {
	return t.fs.OpenFile(e.Path, os.O_CREATE|os.O_RDWR)
}

// Symlink(target, linkPath)
func (t *ext4Target) Symlink(e rootfs.Entry) error { return t.fs.Symlink(e.Link, e.Path) }

// Link(oldPath, newPath)
func (t *ext4Target) Hardlink(e rootfs.Entry) error { return t.fs.Link(e.Link, e.Path) }

func (t *ext4Target) Mknod(e rootfs.Entry) error {
	return t.fs.Mknod(e.Path, mknodMode(e), int(e.Minor()))
}

func (t *ext4Target) Chmod(e rootfs.Entry) error { return t.fs.Chmod(e.Path, e.Mode) }

func (t *ext4Target) Chown(e rootfs.Entry) error { return t.fs.Chown(e.Path, int(e.UID), int(e.GID)) }

func (t *ext4Target) SetTimes(e rootfs.Entry) error {
	return t.fs.Chtimes(e.Path, e.ModTime, e.ModTime, e.ModTime)
}

// mknodMode construye el "mode" estilo Linux que espera mknod(2).
func mknodMode(e rootfs.Entry) uint32 {
	const (
		sIFIFO  = 0o010000
		sIFCHR  = 0o020000
		sIFBLK  = 0o060000
		sIFSOCK = 0o140000
	)
	m := uint32(e.Mode.Perm())
	switch e.Type {
	case rootfs.TypeDevice:
		if e.Mode&os.ModeCharDevice != 0 {
			m |= sIFCHR
		} else {
			m |= sIFBLK
		}
	case rootfs.TypeFifo:
		m |= sIFIFO
	case rootfs.TypeSocket:
		m |= sIFSOCK
	}
	return m
}
