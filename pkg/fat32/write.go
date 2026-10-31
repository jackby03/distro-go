package fat32

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// attrArchive marca un fichero normal.
	attrArchive = 0x20

	// maxFileSize es el limite de FAT32: 4 GiB menos uno.
	maxFileSize = 0xFFFFFFFF

	// maxChain es un tope de seguridad al seguir una cadena de clusters. Evita
	// quedarse en bucle si la FAT esta corrupta.
	maxChain = 1 << 24
)

var (
	// ErrExists indica que ya hay algo con ese nombre.
	ErrExists = errors.New("ya existe una entrada con ese nombre")
	// ErrNoSpace indica que el volumen se ha quedado sin clusters libres.
	ErrNoSpace = errors.New("no quedan clusters libres")
	// ErrFileTooBig indica un fichero que pasa del limite de FAT32.
	ErrFileTooBig = errors.New("el fichero pasa del limite de FAT32")
)

// Mkdir crea un directorio y los padres que falten. Si ya existe, no hace nada.
func (fs *FileSystem) Mkdir(path string) error {
	dir, name := splitPath(path)
	if name == "" {
		return nil // la raiz siempre existe
	}

	parent, err := fs.makeDirs(dir)
	if err != nil {
		return err
	}
	e, found, err := fs.lookup(parent, name)
	if err != nil {
		return err
	}
	if found {
		if e.attr&attrDirectory == 0 {
			return fmt.Errorf("crear el directorio %q: %w", path, ErrExists)
		}
		return nil
	}
	return fs.mkdirIn(parent, name)
}

// Create crea un fichero y devuelve un escritor. Los datos se escriben en
// streaming sobre los clusters que se van asignando.
func (fs *FileSystem) Create(path string) (io.WriteCloser, error) {
	dir, name := splitPath(path)
	if name == "" {
		return nil, fmt.Errorf("crear %q: %w", path, ErrBadName)
	}

	dirCluster, err := fs.makeDirs(dir)
	if err != nil {
		return nil, err
	}
	if _, found, err := fs.lookup(dirCluster, name); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("crear %q: %w", path, ErrExists)
	}

	taken, err := fs.taken(dirCluster)
	if err != nil {
		return nil, err
	}
	plan, err := planName(name, taken)
	if err != nil {
		return nil, err
	}
	if err := fs.addEntry(dirCluster, plan, attrArchive, 0, 0); err != nil {
		return nil, err
	}

	return &fileWriter{
		fs:         fs,
		dirCluster: dirCluster,
		short:      plan.short,
		buf:        make([]byte, 0, fs.geom.clusterSize),
	}, nil
}

// WriteFile copia r en un fichero nuevo.
func (fs *FileSystem) WriteFile(path string, r io.Reader) error {
	w, err := fs.Create(path)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(w, r, buf); err != nil {
		_ = w.Close()
		return fmt.Errorf("escribir %q: %w", path, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("cerrar %q: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------- escritor

type fileWriter struct {
	fs         *FileSystem
	dirCluster uint32
	short      [11]byte

	buf   []byte
	first uint32
	last  uint32
	size  uint32
	done  bool
}

func (w *fileWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("el fichero ya esta cerrado")
	}
	if int64(w.size)+int64(len(p)) > maxFileSize {
		return 0, ErrFileTooBig
	}

	total := 0
	for len(p) > 0 {
		if len(w.buf) == int(w.fs.geom.clusterSize) {
			if err := w.flushCluster(); err != nil {
				return total, err
			}
		}
		room := int(w.fs.geom.clusterSize) - len(w.buf)
		n := len(p)
		if n > room {
			n = room
		}
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		total += n
		w.size += uint32(n)
	}
	return total, nil
}

// Close vuelca el ultimo cluster y parchea la entrada del directorio con el
// primer cluster y el tamano definitivos.
func (w *fileWriter) Close() error {
	if w.done {
		return nil
	}
	w.done = true

	if err := w.flushCluster(); err != nil {
		return err
	}
	return w.fs.patchEntry(w.dirCluster, w.short, w.first, w.size)
}

func (w *fileWriter) flushCluster() error {
	if len(w.buf) == 0 {
		return nil
	}

	c, err := w.fs.allocCluster()
	if err != nil {
		return err
	}
	if w.first == 0 {
		w.first = c
	} else {
		putFAT(w.fs.fat, w.last, c)
	}
	w.last = c

	block := make([]byte, w.fs.geom.clusterSize)
	copy(block, w.buf)
	if err := writeAt(w.fs.dev, w.fs.clusterOffset(c), block); err != nil {
		return fmt.Errorf("escribir el cluster %d: %w", c, err)
	}
	w.buf = w.buf[:0]
	return nil
}

// ------------------------------------------------------------ directorios

// mkdirIn crea un directorio dentro de otro.
func (fs *FileSystem) mkdirIn(parent uint32, name string) error {
	cluster, err := fs.allocCluster()
	if err != nil {
		return err
	}

	block := make([]byte, fs.geom.clusterSize)
	copy(block[0:32], dotEntry(".", cluster))
	copy(block[32:64], dotEntry("..", fs.parentOf(parent)))
	if err := writeAt(fs.dev, fs.clusterOffset(cluster), block); err != nil {
		return fmt.Errorf("escribir el directorio %q: %w", name, err)
	}

	taken, err := fs.taken(parent)
	if err != nil {
		return err
	}
	plan, err := planName(name, taken)
	if err != nil {
		return err
	}
	return fs.addEntry(parent, plan, attrDirectory, cluster, 0)
}

// parentOf devuelve el cluster que debe llevar el ".." de un subdirectorio de
// parent. Los hijos del raiz llevan cero, como manda la especificacion.
func (fs *FileSystem) parentOf(parent uint32) uint32 {
	if parent == rootCluster {
		return 0
	}
	return parent
}

// dotEntry construye una entrada "." o "..".
func dotEntry(name string, cluster uint32) []byte {
	e := make([]byte, 32)
	copy(e[0:11], leftPad(name, 11))
	e[11] = attrDirectory
	put16(e, 20, uint16(cluster>>16))
	put16(e, 26, uint16(cluster&0xFFFF))
	return e
}

// makeDirs resuelve una ruta de directorio creando lo que falte.
func (fs *FileSystem) makeDirs(path string) (uint32, error) {
	path = strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/")
	if path == "" || path == "." {
		return rootCluster, nil
	}

	cur := uint32(rootCluster)
	for _, comp := range strings.Split(path, "/") {
		if comp == "" {
			continue
		}
		child, found, err := fs.findDir(cur, comp)
		if err != nil {
			return 0, err
		}
		if !found {
			if err := fs.mkdirIn(cur, comp); err != nil {
				return 0, err
			}
			child, found, err = fs.findDir(cur, comp)
			if err != nil {
				return 0, err
			}
			if !found {
				return 0, fmt.Errorf("crear %q: la entrada no aparece despues de crearla", comp)
			}
		}
		cur = child
	}
	return cur, nil
}

// lookup busca una entrada por nombre dentro de un directorio.
//
// Salta las entradas de etiqueta de volumen: comparten el espacio de nombres del
// directorio, pero no son ficheros ni directorios. Sin esto, una etiqueta "EFI"
// tapaba el directorio \EFI que exige UEFI.
func (fs *FileSystem) lookup(dirCluster uint32, name string) (dirEntry, bool, error) {
	data, err := fs.readDir(dirCluster)
	if err != nil {
		return dirEntry{}, false, err
	}
	for _, e := range parseDir(data) {
		if e.attr&attrVolumeID != 0 {
			continue
		}
		if strings.EqualFold(e.name, name) {
			return e, true, nil
		}
	}
	return dirEntry{}, false, nil
}

// findDir resuelve un subdirectorio por nombre.
func (fs *FileSystem) findDir(dirCluster uint32, name string) (uint32, bool, error) {
	e, found, err := fs.lookup(dirCluster, name)
	if err != nil || !found {
		return 0, false, err
	}
	if e.attr&attrDirectory == 0 {
		return 0, false, fmt.Errorf("%q existe pero no es un directorio", name)
	}
	return e.cluster, true, nil
}

// taken devuelve los nombres cortos ya usados en un directorio. Incluye la
// etiqueta de volumen, que ocupa un nombre corto aunque no sea un fichero.
func (fs *FileSystem) taken(dirCluster uint32) (map[string]bool, error) {
	data, err := fs.readDir(dirCluster)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, e := range parseDir(data) {
		out[string(e.short[:])] = true
	}
	return out, nil
}

// addEntry escribe un nombre en un directorio, creciendo si hace falta.
func (fs *FileSystem) addEntry(dirCluster uint32, plan namePlan, attr byte, cluster uint32, size uint32) error {
	needed := len(plan.lfn)/32 + 1

	data, err := fs.readDir(dirCluster)
	if err != nil {
		return err
	}

	for attempt := 0; attempt < 8; attempt++ {
		slot, ok := findFreeRun(data, needed)
		if !ok {
			if err := fs.growDir(dirCluster, &data); err != nil {
				return err
			}
			continue
		}

		off := slot * 32
		copy(data[off:off+len(plan.lfn)], plan.lfn)

		shortOff := off + len(plan.lfn)
		e := data[shortOff : shortOff+32]
		copy(e[0:11], plan.short[:])
		e[11] = attr
		put16(e, 20, uint16(cluster>>16))
		put16(e, 26, uint16(cluster&0xFFFF))
		put32(e, 28, size)

		return fs.writeDir(dirCluster, data)
	}
	return fmt.Errorf("no se pudo hacer sitio para la entrada en el directorio %d", dirCluster)
}

// findFreeRun busca el primer tramo de n slots libres consecutivos.
func findFreeRun(data []byte, n int) (int, bool) {
	run := 0
	start := 0
	for slot := 0; slot*32 < len(data); slot++ {
		first := data[slot*32]
		if first == 0x00 || first == 0xE5 {
			if run == 0 {
				start = slot
			}
			run++
			if run >= n {
				return start, true
			}
			continue
		}
		run = 0
	}
	return 0, false
}

// growDir anade un cluster al final de la cadena de un directorio.
func (fs *FileSystem) growDir(dirCluster uint32, data *[]byte) error {
	chain, err := fs.chainOf(dirCluster)
	if err != nil {
		return err
	}
	last := chain[len(chain)-1]

	c, err := fs.allocCluster()
	if err != nil {
		return err
	}
	putFAT(fs.fat, last, c)

	block := make([]byte, fs.geom.clusterSize)
	if err := writeAt(fs.dev, fs.clusterOffset(c), block); err != nil {
		return fmt.Errorf("limpiar el cluster %d del directorio: %w", c, err)
	}
	*data = append(*data, block...)
	return nil
}

// patchEntry actualiza el primer cluster y el tamano de un fichero.
func (fs *FileSystem) patchEntry(dirCluster uint32, short [11]byte, first, size uint32) error {
	data, err := fs.readDir(dirCluster)
	if err != nil {
		return err
	}
	for _, e := range parseDir(data) {
		if e.short != short {
			continue
		}
		off := e.slot * 32
		put16(data, off+20, uint16(first>>16))
		put16(data, off+26, uint16(first&0xFFFF))
		put32(data, off+28, size)
		return fs.writeDir(dirCluster, data)
	}
	return fmt.Errorf("no se encontro la entrada %q al cerrar el fichero", string(short[:]))
}

// --------------------------------------------------------------- clusters

// allocCluster reserva un cluster y lo marca como fin de cadena.
func (fs *FileSystem) allocCluster() (uint32, error) {
	last := uint32(fs.geom.clusters) + 1
	for c := fs.nextFree; c <= last; c++ {
		if getFAT(fs.fat, c) != 0 {
			continue
		}
		putFAT(fs.fat, c, eocMarker)
		fs.nextFree = c + 1
		return c, nil
	}
	return 0, ErrNoSpace
}

// chainOf sigue la cadena de clusters de un fichero o directorio.
func (fs *FileSystem) chainOf(first uint32) ([]uint32, error) {
	if first == 0 {
		return nil, nil
	}
	var out []uint32
	for c := first; ; {
		out = append(out, c)
		if len(out) > maxChain {
			return nil, fmt.Errorf("cadena de clusters demasiado larga desde %d", first)
		}
		next := getFAT(fs.fat, c)
		if next == eocMarker || next == 0 || next >= 0x0FFFFFF7 {
			break
		}
		if next < rootCluster {
			return nil, fmt.Errorf("cadena de clusters invalida desde %d: apunta a %d", first, next)
		}
		c = next
	}
	return out, nil
}

// readDir lee el contenido completo de un directorio.
func (fs *FileSystem) readDir(cluster uint32) ([]byte, error) {
	chain, err := fs.chainOf(cluster)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(chain)*int(fs.geom.clusterSize))
	block := make([]byte, fs.geom.clusterSize)
	for _, c := range chain {
		if _, err := fs.dev.ReadAt(block, fs.clusterOffset(c)); err != nil {
			return nil, fmt.Errorf("leer el cluster %d del directorio: %w", c, err)
		}
		out = append(out, block...)
	}
	return out, nil
}

// writeDir escribe el contenido de un directorio en su cadena de clusters.
func (fs *FileSystem) writeDir(cluster uint32, data []byte) error {
	chain, err := fs.chainOf(cluster)
	if err != nil {
		return err
	}
	block := make([]byte, fs.geom.clusterSize)
	for i, c := range chain {
		off := i * int(fs.geom.clusterSize)
		if off >= len(data) {
			break
		}
		end := off + int(fs.geom.clusterSize)
		if end > len(data) {
			end = len(data)
		}
		for j := range block {
			block[j] = 0
		}
		copy(block, data[off:end])
		if err := writeAt(fs.dev, fs.clusterOffset(c), block); err != nil {
			return fmt.Errorf("escribir el cluster %d del directorio: %w", c, err)
		}
	}
	return nil
}

// freeClusters cuenta los clusters libres recorriendo la FAT.
func (fs *FileSystem) freeClusters() uint32 {
	var free uint32
	last := uint32(fs.geom.clusters) + 1
	for c := uint32(rootCluster); c <= last; c++ {
		if getFAT(fs.fat, c) == 0 {
			free++
		}
	}
	return free
}

// ------------------------------------------------------------------ lectura

// dirEntry es una entrada de directorio ya interpretada.
type dirEntry struct {
	short   [11]byte
	name    string
	attr    byte
	cluster uint32
	size    uint32
	slot    int
	slots   int
}

// parseDir interpreta las entradas de un directorio, juntando cada nombre largo
// con su nombre corto.
func parseDir(data []byte) []dirEntry {
	var out []dirEntry
	var units []uint16
	var lfnSlots int

	for slot := 0; slot*32+32 <= len(data); slot++ {
		e := data[slot*32 : slot*32+32]
		switch e[0] {
		case 0x00:
			return out
		case 0xE5:
			units, lfnSlots = nil, 0
			continue
		}

		if e[11] == attrLFN {
			var chunk []uint16
			for _, off := range lfnOffsets {
				chunk = append(chunk, binary.LittleEndian.Uint16(e[off:off+2]))
			}
			// En disco llegan al reves: la primera que aparece es la ultima del
			// nombre.
			units = append(chunk, units...)
			lfnSlots++
			continue
		}

		var short [11]byte
		copy(short[:], e[0:11])

		d := dirEntry{
			short:   short,
			attr:    e[11],
			cluster: uint32(binary.LittleEndian.Uint16(e[26:])) | uint32(binary.LittleEndian.Uint16(e[20:]))<<16,
			size:    binary.LittleEndian.Uint32(e[28:]),
			slot:    slot,
			slots:   lfnSlots + 1,
			name:    nameFromShort(short),
		}
		if lfnSlots > 0 {
			if long := utf16String(units); long != "" {
				d.name = long
			}
		}
		out = append(out, d)
		units, lfnSlots = nil, 0
	}
	return out
}

// nameFromShort convierte un nombre 8.3 en texto.
func nameFromShort(short [11]byte) string {
	base := strings.TrimRight(string(short[0:8]), " ")
	ext := strings.TrimRight(string(short[8:11]), " ")
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// utf16String monta una cadena y la corta en el terminador.
func utf16String(units []uint16) string {
	for i, u := range units {
		if u == 0x0000 || u == 0xFFFF {
			units = units[:i]
			break
		}
	}
	runes := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u >= 0xD800 && u < 0xDC00 && i+1 < len(units) {
			// Par suplente: recomponer el caracter fuera del plano basico.
			hi, lo := rune(u), rune(units[i+1])
			runes = append(runes, 0x10000+(hi-0xD800)<<10+(lo-0xDC00))
			i++
			continue
		}
		runes = append(runes, rune(u))
	}
	return string(runes)
}

// splitPath separa directorio y nombre.
func splitPath(path string) (dir, name string) {
	clean := strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/")
	if clean == "" {
		return "", ""
	}
	if i := strings.LastIndexByte(clean, '/'); i >= 0 {
		return clean[:i], clean[i+1:]
	}
	return "", clean
}
