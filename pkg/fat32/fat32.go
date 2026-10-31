package fat32

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"distrogo/pkg/blockdev"
)

const (
	// reservedSectors es lo que se deja antes de la primera FAT. 32 es el valor
	// que usa Windows: cubre el sector de arranque, la FSInfo y su respaldo.
	reservedSectors = 32

	// numFATs: FAT32 siempre lleva dos copias.
	numFATs = 2

	// rootCluster es el cluster del directorio raiz. El primero util es el 2.
	rootCluster = 2

	// minClusters y maxClusters acotan el numero de clusters para que cualquier
	// lector reconozca el volumen como FAT32 y no como FAT16.
	//
	// fsck.fat y el firmware deciden el tipo por el numero de clusters: por
	// debajo de 4085 es FAT12, por debajo de 65525 es FAT16.
	minClusters = 65525
	maxClusters = 0x0FFFFFF4

	eocMarker = 0x0FFFFFFF
	mediaByte = 0xF8

	// defaultTime es la marca de tiempo que se pone a todo. Al firmware le da
	// igual y hace el resultado reproducible.
	defaultTime = "1980-01-01T00:00:00Z"
)

var (
	// ErrTooSmall indica un volumen que no da para FAT32.
	ErrTooSmall = errors.New("el volumen es demasiado pequeno para FAT32")
	// ErrBadOption indica una opcion invalida.
	ErrBadOption = errors.New("opcion invalida")
)

// Options ajusta el formateo.
type Options struct {
	// Label es la etiqueta del volumen, hasta 11 caracteres. Vacio usa "DISTROGO".
	Label string
	// ClusterSize en bytes. Cero elige el tamano segun el volumen, imitando la
	// tabla de Windows.
	ClusterSize int
	// VolumeID es el identificador de volumen. Cero genera uno aleatorio; los
	// tests fijan uno para que el resultado sea reproducible.
	VolumeID uint32
}

// Info es la geometria resultante, util para informar y para testear.
type Info struct {
	SectorSize    int
	ClusterSize   int
	TotalSectors  uint64
	FatSectors    uint64
	ReservedStart uint64
	DataStart     uint64
	Clusters      uint64
	Label         string
	VolumeID      uint32
}

// FileSystem es un volumen FAT32 montado para escritura.
type FileSystem struct {
	dev  blockdev.Writable
	geom geometry
	info Info

	// fat es la tabla en memoria. Se escribe en las dos copias al cerrar.
	fat      []byte
	nextFree uint32
}

type geometry struct {
	sectorSize        uint64
	sectorsPerCluster uint64
	clusterSize       uint64
	reservedSectors   uint64
	fatSectors        uint64
	totalSectors      uint64
	clusters          uint64
	dataStart         uint64
}

// Format crea un volumen FAT32 vacio en el dispositivo.
//
// El dispositivo tiene que ser escribible y su tamano, el del volumen. No se
// borra el resto del disco: solo se escribe el area de sistema y el cluster del
// directorio raiz.
func Format(dev blockdev.Writable, opts Options) (*FileSystem, error) {
	size := dev.Size()
	sectorSize := dev.SectorSize()
	if sectorSize < 512 || sectorSize > 4096 || sectorSize&(sectorSize-1) != 0 {
		return nil, fmt.Errorf("%w: sector de %d bytes", ErrBadOption, sectorSize)
	}

	label, err := normalizeLabel(opts.Label)
	if err != nil {
		return nil, err
	}

	geom, err := chooseGeometry(size, sectorSize, opts.ClusterSize)
	if err != nil {
		return nil, err
	}

	volumeID := opts.VolumeID
	if volumeID == 0 {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fmt.Errorf("generar el identificador de volumen: %w", err)
		}
		volumeID = binary.LittleEndian.Uint32(b[:])
	}

	fs := &FileSystem{
		dev:  dev,
		geom: geom,
		info: Info{
			SectorSize:    int(geom.sectorSize),
			ClusterSize:   int(geom.clusterSize),
			TotalSectors:  geom.totalSectors,
			FatSectors:    geom.fatSectors,
			ReservedStart: geom.reservedSectors,
			DataStart:     geom.dataStart,
			Clusters:      geom.clusters,
			Label:         label,
			VolumeID:      volumeID,
		},
		fat:      make([]byte, geom.fatSectors*geom.sectorSize),
		nextFree: rootCluster + 1,
	}

	// Entradas reservadas de la FAT: la primera lleva el byte de medio, la
	// segunda marca fin de cadena y el bit de desmontaje limpio.
	putFAT(fs.fat, 0, 0x0FFFFF00|mediaByte)
	putFAT(fs.fat, 1, eocMarker)
	// El directorio raiz ocupa el cluster 2 y es una cadena de un solo cluster.
	putFAT(fs.fat, rootCluster, eocMarker)

	if err := fs.ClearSystemArea(); err != nil {
		return nil, err
	}
	return fs, nil
}

// Info devuelve la geometria del volumen.
func (fs *FileSystem) Info() Info { return fs.info }

// Label devuelve la etiqueta.
func (fs *FileSystem) Label() string { return fs.info.Label }

// Close vuelca la tabla FAT, recalcula el resumen de clusters libres y
// sincroniza. Hasta que se llama, los cambios solo viven en memoria.
func (fs *FileSystem) Close() error {
	if err := fs.flushFAT(); err != nil {
		return err
	}
	if err := fs.flushFSInfo(); err != nil {
		return err
	}
	if err := fs.dev.Sync(); err != nil {
		return fmt.Errorf("sincronizar el volumen: %w", err)
	}
	return nil
}

// flushFAT escribe las dos copias de la tabla.
func (fs *FileSystem) flushFAT() error {
	for i := uint64(0); i < numFATs; i++ {
		off := int64((fs.geom.reservedSectors + i*fs.geom.fatSectors) * fs.geom.sectorSize)
		if err := writeAt(fs.dev, off, fs.fat); err != nil {
			return fmt.Errorf("escribir la copia %d de la FAT: %w", i+1, err)
		}
	}
	return nil
}

// flushFSInfo reescribe la informacion libre y su respaldo con el numero real de
// clusters. fsck.fat comprueba ese numero, asi que no vale dejarlo obsoleto.
func (fs *FileSystem) flushFSInfo() error {
	info := fs.fsInfoSector(fs.freeClusters())
	if err := writeAt(fs.dev, int64(fs.geom.sectorSize), info); err != nil {
		return fmt.Errorf("escribir la FSInfo: %w", err)
	}
	if err := writeAt(fs.dev, int64(7*fs.geom.sectorSize), info); err != nil {
		return fmt.Errorf("escribir el respaldo de la FSInfo: %w", err)
	}
	return nil
}

// ClearSystemArea escribe el sector de arranque, la FSInfo, sus respaldos, las
// dos FAT y el cluster del directorio raiz. Deja el volumen valido y vacio.
func (fs *FileSystem) ClearSystemArea() error {
	sectorSize := fs.geom.sectorSize

	// Se pone a cero el area de sistema completa: si el disco tenia otra tabla,
	// no queremos restos.
	blank := make([]byte, sectorSize)
	for sec := uint64(0); sec < fs.geom.dataStart; sec++ {
		if err := writeAt(fs.dev, int64(sec*sectorSize), blank); err != nil {
			return fmt.Errorf("limpiar el sector %d: %w", sec, err)
		}
	}

	boot := fs.bootSector()
	if err := writeAt(fs.dev, 0, boot); err != nil {
		return fmt.Errorf("escribir el sector de arranque: %w", err)
	}
	// Respaldo del sector de arranque en el sector 6, como manda la tradicion.
	if err := writeAt(fs.dev, int64(6*sectorSize), boot); err != nil {
		return fmt.Errorf("escribir el respaldo del sector de arranque: %w", err)
	}

	if err := fs.flushFAT(); err != nil {
		return err
	}
	if err := fs.flushFSInfo(); err != nil {
		return err
	}

	// Directorio raiz: solo la entrada de etiqueta de volumen. FAT32 guarda la
	// etiqueta en dos sitios, el BPB y esta entrada; si no coinciden, fsck.fat
	// avisa y la borra del BPB.
	root := make([]byte, fs.geom.clusterSize)
	copy(root[0:32], fs.labelEntry())
	if err := writeAt(fs.dev, fs.clusterOffset(rootCluster), root); err != nil {
		return fmt.Errorf("escribir el directorio raiz: %w", err)
	}
	if err := fs.dev.Sync(); err != nil {
		return fmt.Errorf("sincronizar el volumen: %w", err)
	}
	return nil
}

// bootSector construye el BPB de FAT32, 512 bytes.
func (fs *FileSystem) bootSector() []byte {
	b := make([]byte, 512)

	copy(b[0:3], []byte{0xEB, 0x58, 0x90}) // salto corto y NOP, convencion
	copy(b[3:11], []byte("DISTROGO"))

	put16(b, 0x0B, uint16(fs.geom.sectorSize))
	b[0x0D] = byte(fs.geom.sectorsPerCluster)
	put16(b, 0x0E, uint16(fs.geom.reservedSectors))
	b[0x10] = numFATs
	put16(b, 0x11, 0) // entradas de raiz: cero en FAT32
	put16(b, 0x13, 0) // total de sectores en 16 bits: cero en FAT32
	b[0x15] = mediaByte
	put16(b, 0x16, 0) // tamano de FAT en 16 bits: cero en FAT32
	put16(b, 0x18, 63)
	put16(b, 0x1A, 255)
	put32(b, 0x1C, 0) // sectores ocultos
	put32(b, 0x20, uint32(fs.geom.totalSectors))
	put32(b, 0x24, uint32(fs.geom.fatSectors))
	put16(b, 0x28, 0) // flags extendidos: sin duplicacion activa
	put16(b, 0x2A, 0) // version del sistema de ficheros: 0.0
	put32(b, 0x2C, rootCluster)
	put16(b, 0x30, 1) // sector de la FSInfo
	put16(b, 0x32, 6) // respaldo del sector de arranque

	b[0x40] = 0x80 // unidad de arranque
	b[0x42] = 0x29 // firma extendida
	put32(b, 0x43, fs.info.VolumeID)
	copy(b[0x47:0x52], leftPad(fs.info.Label, 11))
	copy(b[0x52:0x5A], []byte("FAT32   "))

	b[510], b[511] = 0x55, 0xAA
	return b
}

// fsInfoSector construye el sector de informacion libre con el numero de
// clusters que se le pase.
func (fs *FileSystem) fsInfoSector(free uint32) []byte {
	b := make([]byte, 512)

	put32(b, 0, 0x41615252) // firma inicial
	put32(b, 484, 0x61417272)
	put32(b, 488, free)
	put32(b, 492, fs.nextFree)
	put32(b, 508, 0xAA550000)

	return b
}

// labelEntry construye la entrada de etiqueta de volumen, que ocupa el primer
// hueco del directorio raiz. Su nombre son 11 bytes rellenados con espacios y su
// atributo es 0x08; el resto de campos va a cero.
func (fs *FileSystem) labelEntry() []byte {
	e := make([]byte, 32)
	copy(e[0:11], leftPad(fs.info.Label, 11))
	e[11] = 0x08 // atributo de etiqueta de volumen
	return e
}

// clusterOffset devuelve el byte donde empieza un cluster.
func (fs *FileSystem) clusterOffset(cluster uint32) int64 {
	sec := fs.geom.dataStart + uint64(cluster-rootCluster)*fs.geom.sectorsPerCluster
	return int64(sec * fs.geom.sectorSize)
}

// chooseGeometry resuelve el tamano de cluster y el de la FAT.
func chooseGeometry(size int64, sectorSize int, wanted int) (geometry, error) {
	if size <= 0 {
		return geometry{}, fmt.Errorf("%w: tamano %d", ErrBadOption, size)
	}

	candidates := []int{wanted}
	if wanted <= 0 {
		candidates = defaultClusterSizes(size)
	}

	var lastErr error
	for _, c := range candidates {
		if c < sectorSize || c > 32768 || c&(c-1) != 0 {
			lastErr = fmt.Errorf("%w: cluster de %d bytes", ErrBadOption, c)
			continue
		}
		g, err := solveGeometry(size, sectorSize, c)
		if err != nil {
			lastErr = err
			continue
		}
		return g, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: %d bytes", ErrTooSmall, size)
	}
	return geometry{}, lastErr
}

// defaultClusterSizes imita la tabla de Windows para FAT32 y anade tamanos mas
// pequenos por si el volumen no llega al minimo de clusters.
func defaultClusterSizes(size int64) []int {
	var first int
	switch {
	case size < 260<<20:
		first = 512
	case size < 8<<30:
		first = 4096
	case size < 16<<30:
		first = 8192
	case size < 32<<30:
		first = 16384
	default:
		first = 32768
	}

	// Del elegido hacia abajo: si no caben los clusters minimos, se prueba con
	// clusters mas pequenos antes de rendirse.
	out := []int{first}
	for c := first; c > 512; c /= 2 {
		out = append(out, c/2)
	}
	return out
}

// solveGeometry calcula la geometria para un tamano de cluster, resolviendo la
// dependencia circular entre el tamano de la FAT y el numero de clusters.
func solveGeometry(size int64, sectorSize, clusterSize int) (geometry, error) {
	total := uint64(size) / uint64(sectorSize)
	if total == 0 {
		return geometry{}, fmt.Errorf("%w: %d bytes", ErrTooSmall, size)
	}
	spc := uint64(clusterSize) / uint64(sectorSize)
	if spc == 0 {
		return geometry{}, fmt.Errorf("%w: cluster de %d bytes", ErrBadOption, clusterSize)
	}

	fatSectors := uint64(1)
	for i := 0; i < 32; i++ {
		if total <= reservedSectors+numFATs*fatSectors {
			return geometry{}, fmt.Errorf("%w: %d bytes", ErrTooSmall, size)
		}
		dataSectors := total - reservedSectors - numFATs*fatSectors
		clusters := dataSectors / spc
		needed := (clusters + 2) * 4
		needed = (needed + uint64(sectorSize) - 1) / uint64(sectorSize)
		if needed <= fatSectors {
			break
		}
		fatSectors = needed
	}

	dataSectors := total - reservedSectors - numFATs*fatSectors
	clusters := dataSectors / spc

	switch {
	case clusters < minClusters:
		return geometry{}, fmt.Errorf("%w: %d clusters, minimo %d", ErrTooSmall, clusters, minClusters)
	case clusters > maxClusters:
		return geometry{}, fmt.Errorf("%w: %d clusters, maximo %d", ErrBadOption, clusters, maxClusters)
	}

	return geometry{
		sectorSize:        uint64(sectorSize),
		sectorsPerCluster: spc,
		clusterSize:       uint64(clusterSize),
		reservedSectors:   reservedSectors,
		fatSectors:        fatSectors,
		totalSectors:      total,
		clusters:          clusters,
		dataStart:         reservedSectors + numFATs*fatSectors,
	}, nil
}

// normalizeLabel deja la etiqueta en mayusculas, rellenada a 11 caracteres.
func normalizeLabel(label string) (string, error) {
	l := strings.ToUpper(strings.TrimSpace(label))
	if l == "" {
		l = "DISTROGO"
	}
	if len(l) > 11 {
		return "", fmt.Errorf("%w: la etiqueta %q pasa de 11 caracteres", ErrBadOption, label)
	}
	for _, r := range l {
		if strings.ContainsRune(`"*/:<>?\|.`+"\x00", r) {
			return "", fmt.Errorf("%w: la etiqueta contiene %q", ErrBadOption, r)
		}
	}
	return l, nil
}

// leftPad rellena por la derecha con espacios hasta n.
func leftPad(s string, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	copy(out, s)
	return out
}

func putFAT(fat []byte, cluster uint32, value uint32) {
	if off := int(cluster) * 4; off+4 <= len(fat) {
		binary.LittleEndian.PutUint32(fat[off:off+4], value)
	}
}

func getFAT(fat []byte, cluster uint32) uint32 {
	if off := int(cluster) * 4; off+4 <= len(fat) {
		return binary.LittleEndian.Uint32(fat[off:off+4]) & 0x0FFFFFFF
	}
	return eocMarker
}

func put16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:off+2], v) }
func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:off+4], v) }

func writeAt(dev blockdev.Writable, off int64, data []byte) error {
	n, err := dev.WriteAt(data, off)
	if err != nil {
		return fmt.Errorf("offset %d: %w", off, err)
	}
	if n != len(data) {
		return fmt.Errorf("offset %d: escritos %d de %d bytes", off, n, len(data))
	}
	return nil
}
