package blockdev

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrSectionRange indica un rango de seccion fuera del dispositivo.
var ErrSectionRange = errors.New("rango de seccion fuera del dispositivo")

// Section es una vista de un dispositivo limitada a un rango de bytes: lo que
// hace falta para tratar una particion como si fuera un dispositivo propio.
//
// Cerrar una seccion NO cierra el dispositivo subyacente: la seccion es una
// ventana, no un dueno. El que abrio el dispositivo es quien lo cierra.
//
// Implementa Writable, pero WriteAt devuelve ErrNotWritable si el dispositivo
// subyacente no admite escritura.
type Section struct {
	dev    Device
	offset int64
	size   int64
	path   string
}

var (
	_ Device   = (*Section)(nil)
	_ Writable = (*Section)(nil)
)

// NewSection recorta un dispositivo. offset y size se miden en bytes y tienen
// que caer dentro del dispositivo.
func NewSection(dev Device, offset, size int64) (*Section, error) {
	if dev == nil {
		return nil, fmt.Errorf("seccion sin dispositivo")
	}
	if offset < 0 || size <= 0 {
		return nil, fmt.Errorf("%w: offset %d, tamano %d", ErrSectionRange, offset, size)
	}
	if offset+size > dev.Size() {
		return nil, fmt.Errorf("%w: offset %d + tamano %d pasan de %d", ErrSectionRange, offset, size, dev.Size())
	}
	return &Section{
		dev:    dev,
		offset: offset,
		size:   size,
		path:   describe(dev),
	}, nil
}

func (s *Section) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("leer seccion de %s en offset %d: %w", s.path, off, os.ErrInvalid)
	}
	if off >= s.size {
		return 0, io.EOF
	}
	if off+int64(len(p)) > s.size {
		n, err := s.dev.ReadAt(p[:s.size-off], s.offset+off)
		if err != nil {
			return n, fmt.Errorf("leer seccion de %s en offset %d: %w", s.path, off, err)
		}
		return n, io.EOF
	}
	n, err := s.dev.ReadAt(p, s.offset+off)
	if err != nil {
		return n, fmt.Errorf("leer seccion de %s en offset %d: %w", s.path, off, err)
	}
	return n, nil
}

func (s *Section) WriteAt(p []byte, off int64) (int, error) {
	w, ok := s.dev.(Writable)
	if !ok {
		return 0, fmt.Errorf("escribir seccion de %s: %w", s.path, ErrNotWritable)
	}
	if off < 0 {
		return 0, fmt.Errorf("escribir seccion de %s en offset %d: %w", s.path, off, os.ErrInvalid)
	}
	if off+int64(len(p)) > s.size {
		return 0, fmt.Errorf("escribir %d bytes en offset %d de una seccion de %d: %w",
			len(p), off, s.size, ErrOutOfRange)
	}
	n, err := w.WriteAt(p, s.offset+off)
	if err != nil {
		return n, fmt.Errorf("escribir seccion de %s en offset %d: %w", s.path, off, err)
	}
	return n, nil
}

// Size es el tamano de la seccion, no el del dispositivo.
func (s *Section) Size() int64 { return s.size }

// SectorSize se hereda del dispositivo: una particion no cambia el sector.
func (s *Section) SectorSize() int { return s.dev.SectorSize() }

// Offset es donde empieza la seccion dentro del dispositivo.
func (s *Section) Offset() int64 { return s.offset }

func (s *Section) Sync() error {
	if err := s.dev.Sync(); err != nil {
		return fmt.Errorf("sincronizar seccion de %s: %w", s.path, err)
	}
	return nil
}

// Close no hace nada: la seccion no es duena del dispositivo.
func (s *Section) Close() error { return nil }

func describe(dev Device) string {
	if i, ok := dev.(*Image); ok {
		return i.path
	}
	return "dispositivo"
}
