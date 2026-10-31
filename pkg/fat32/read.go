package fat32

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"distrogo/pkg/blockdev"
)

// ErrNotFAT32 indica que el dispositivo no contiene un volumen FAT32.
var ErrNotFAT32 = errors.New("el dispositivo no contiene un volumen FAT32")

// Entry es una entrada de un directorio, ya interpretada.
type Entry struct {
	Name  string
	Size  int64
	IsDir bool
}

// Open monta un volumen FAT32 existente leyendo su BPB y su tabla.
//
// El dispositivo tiene que ser escribible porque el mismo tipo sirve para
// escribir; para solo leer, abrir la imagen en modo lectura no basta.
func Open(dev blockdev.Writable) (*FileSystem, error) {
	if dev.Size() < 512 {
		return nil, fmt.Errorf("%w: %d bytes", ErrNotFAT32, dev.Size())
	}

	boot := make([]byte, 512)
	if _, err := dev.ReadAt(boot, 0); err != nil {
		return nil, fmt.Errorf("leer el sector de arranque: %w", err)
	}
	if boot[510] != 0x55 || boot[511] != 0xAA || string(boot[0x52:0x5A]) != "FAT32   " {
		return nil, ErrNotFAT32
	}

	sectorSize := int(binary.LittleEndian.Uint16(boot[0x0B:]))
	spc := uint64(boot[0x0D])
	reserved := uint64(binary.LittleEndian.Uint16(boot[0x0E:]))
	fats := uint64(boot[0x10])
	fatSectors := uint64(binary.LittleEndian.Uint32(boot[0x24:]))
	total := uint64(binary.LittleEndian.Uint32(boot[0x20:]))

	if sectorSize < 512 || spc == 0 || fatSectors == 0 || total == 0 {
		return nil, fmt.Errorf("%w: BPB incoherente", ErrNotFAT32)
	}
	if reserved+fats*fatSectors >= total {
		return nil, fmt.Errorf("%w: area de sistema mayor que el volumen", ErrNotFAT32)
	}

	clusters := (total - reserved - fats*fatSectors) / spc

	fat := make([]byte, fatSectors*uint64(sectorSize))
	if _, err := dev.ReadAt(fat, int64(reserved)*int64(sectorSize)); err != nil {
		return nil, fmt.Errorf("leer la tabla FAT: %w", err)
	}

	return &FileSystem{
		dev: dev,
		geom: geometry{
			sectorSize:        uint64(sectorSize),
			sectorsPerCluster: spc,
			clusterSize:       spc * uint64(sectorSize),
			reservedSectors:   reserved,
			fatSectors:        fatSectors,
			totalSectors:      total,
			clusters:          clusters,
			dataStart:         reserved + fats*fatSectors,
		},
		info: Info{
			SectorSize:   sectorSize,
			ClusterSize:  int(spc) * sectorSize,
			TotalSectors: total,
			FatSectors:   fatSectors,
			DataStart:    reserved + fats*fatSectors,
			Clusters:     clusters,
			Label:        strings.TrimSpace(string(boot[0x47:0x52])),
			VolumeID:     binary.LittleEndian.Uint32(boot[0x43:0x47]),
		},
		fat:      fat,
		nextFree: rootCluster + 1,
	}, nil
}

// FreeBytes es el espacio libre del volumen.
func (fs *FileSystem) FreeBytes() int64 {
	return int64(fs.freeClusters()) * int64(fs.geom.clusterSize)
}

// Stat devuelve la entrada con ese nombre.
func (fs *FileSystem) Stat(path string) (Entry, error) {
	e, found, err := fs.resolve(path)
	if err != nil {
		return Entry{}, err
	}
	if !found {
		return Entry{}, fmt.Errorf("%q: %w", path, ErrNotFoundInVolume)
	}
	return Entry{Name: e.name, Size: int64(e.size), IsDir: e.attr&attrDirectory != 0}, nil
}

// ErrNotFoundInVolume indica que la ruta no existe en el volumen.
var ErrNotFoundInVolume = errors.New("ruta inexistente en el volumen")

// resolve busca una ruta completa.
func (fs *FileSystem) resolve(path string) (dirEntry, bool, error) {
	dir, name := splitPath(path)
	cluster := uint32(rootCluster)

	for _, comp := range strings.Split(dir, "/") {
		if comp == "" {
			continue
		}
		next, found, err := fs.findDir(cluster, comp)
		if err != nil {
			return dirEntry{}, false, err
		}
		if !found {
			return dirEntry{}, false, fmt.Errorf("%q: %w", comp, ErrNotFoundInVolume)
		}
		cluster = next
	}
	return fs.lookup(cluster, name)
}

// OpenFile abre un fichero para leerlo de forma secuencial.
func (fs *FileSystem) OpenFile(path string) (io.ReadCloser, int64, error) {
	e, found, err := fs.resolve(path)
	if err != nil {
		return nil, 0, err
	}
	if !found {
		return nil, 0, fmt.Errorf("%q: %w", path, ErrNotFoundInVolume)
	}
	if e.attr&attrDirectory != 0 {
		return nil, 0, fmt.Errorf("%q es un directorio", path)
	}
	if e.cluster == 0 || e.size == 0 {
		return io.NopCloser(strings.NewReader("")), 0, nil
	}

	chain, err := fs.chainOf(e.cluster)
	if err != nil {
		return nil, 0, err
	}
	return &fileReader{fs: fs, chain: chain, size: int64(e.size)}, int64(e.size), nil
}

// List devuelve el contenido de un directorio.
func (fs *FileSystem) List(path string) ([]Entry, error) {
	cluster := uint32(rootCluster)
	if clean := strings.Trim(path, "/"); clean != "" && clean != "." {
		for _, comp := range strings.Split(clean, "/") {
			next, found, err := fs.findDir(cluster, comp)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("%q: %w", comp, ErrNotFoundInVolume)
			}
			cluster = next
		}
	}

	data, err := fs.readDir(cluster)
	if err != nil {
		return nil, err
	}

	var out []Entry
	for _, e := range parseDir(data) {
		if e.attr&attrVolumeID != 0 || e.name == "." || e.name == ".." {
			continue
		}
		out = append(out, Entry{Name: e.name, Size: int64(e.size), IsDir: e.attr&attrDirectory != 0})
	}
	return out, nil
}

// fileReader lee un fichero cluster a cluster, sin cargarlo entero en memoria.
type fileReader struct {
	fs    *FileSystem
	chain []uint32
	size  int64
	pos   int64
	buf   []byte
}

func (r *fileReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}

	clusterSize := int64(r.fs.geom.clusterSize)
	total := 0

	for total < len(p) && r.pos < r.size {
		idx := r.pos / clusterSize
		off := r.pos % clusterSize
		if idx >= int64(len(r.chain)) {
			return total, io.ErrUnexpectedEOF
		}
		if r.buf == nil {
			r.buf = make([]byte, clusterSize)
		}
		if _, err := r.fs.dev.ReadAt(r.buf, r.fs.clusterOffset(r.chain[idx])); err != nil {
			return total, fmt.Errorf("leer el cluster %d: %w", r.chain[idx], err)
		}

		n := int64(len(p) - total)
		if n > r.size-r.pos {
			n = r.size - r.pos
		}
		if n > clusterSize-off {
			n = clusterSize - off
		}
		copy(p[total:], r.buf[off:off+n])
		total += int(n)
		r.pos += n
	}
	return total, nil
}

func (r *fileReader) Close() error { return nil }
