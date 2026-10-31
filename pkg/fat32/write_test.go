package fat32

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"distrogo/pkg/blockdev"
)

// reload vuelve a montar el volumen leyendo el BPB y la FAT del dispositivo. Es
// la comprobacion que importa: lo que se lee del disco, no lo que hay en memoria.
func reload(t *testing.T, img *blockdev.Image) *FileSystem {
	t.Helper()

	boot := readBytes(t, img, 0, 512)
	sectorSize := int(binary.LittleEndian.Uint16(boot[0x0B:]))
	spc := uint64(boot[0x0D])
	reserved := uint64(binary.LittleEndian.Uint16(boot[0x0E:]))
	fatSectors := uint64(binary.LittleEndian.Uint32(boot[0x24:]))
	total := uint64(binary.LittleEndian.Uint32(boot[0x20:]))
	clusters := (total - reserved - numFATs*fatSectors) / spc

	fat := readBytes(t, img, int64(reserved)*int64(sectorSize), int(fatSectors)*sectorSize)

	return &FileSystem{
		dev: img,
		geom: geometry{
			sectorSize:        uint64(sectorSize),
			sectorsPerCluster: spc,
			clusterSize:       spc * uint64(sectorSize),
			reservedSectors:   reserved,
			fatSectors:        fatSectors,
			totalSectors:      total,
			clusters:          clusters,
			dataStart:         reserved + numFATs*fatSectors,
		},
		info:     Info{SectorSize: sectorSize, ClusterSize: int(spc) * sectorSize, Clusters: clusters},
		fat:      fat,
		nextFree: rootCluster + 1,
	}
}

// readPath devuelve los datos de un fichero siguiendo directorios y cadena.
func readPath(t *testing.T, fs *FileSystem, path string) []byte {
	t.Helper()

	dir, name := splitPath(path)
	cluster := uint32(rootCluster)
	if dir != "" {
		for _, comp := range strings.Split(dir, "/") {
			next, found, err := fs.findDir(cluster, comp)
			if err != nil {
				t.Fatalf("resolver %q: %v", comp, err)
			}
			if !found {
				t.Fatalf("no existe el directorio %q en %q", comp, path)
			}
			cluster = next
		}
	}

	entries, err := fs.readDir(cluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	for _, e := range parseDir(entries) {
		if !strings.EqualFold(e.name, name) {
			continue
		}
		chain, err := fs.chainOf(e.cluster)
		if err != nil {
			t.Fatalf("chainOf: %v", err)
		}
		var out []byte
		block := make([]byte, fs.geom.clusterSize)
		for _, c := range chain {
			if _, err := fs.dev.ReadAt(block, fs.clusterOffset(c)); err != nil {
				t.Fatalf("leer el cluster %d: %v", c, err)
			}
			out = append(out, block...)
		}
		if uint32(len(out)) < e.size {
			t.Fatalf("el fichero %q dice %d bytes y solo hay %d", path, e.size, len(out))
		}
		return out[:e.size]
	}
	t.Fatalf("no existe el fichero %q", path)
	return nil
}

// statPath devuelve la entrada de un nombre.
func statPath(t *testing.T, fs *FileSystem, path string) dirEntry {
	t.Helper()

	dir, name := splitPath(path)
	cluster := uint32(rootCluster)
	if dir != "" {
		for _, comp := range strings.Split(dir, "/") {
			next, found, err := fs.findDir(cluster, comp)
			if err != nil || !found {
				t.Fatalf("directorio %q inaccesible", comp)
			}
			cluster = next
		}
	}
	data, err := fs.readDir(cluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	for _, e := range parseDir(data) {
		if strings.EqualFold(e.name, name) {
			return e
		}
	}
	t.Fatalf("no existe %q", path)
	return dirEntry{}
}

func TestMkdirCreatesNestedDirectories(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	if err := fs.Mkdir("EFI/boot"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)

	efi := statPath(t, disk, "EFI")
	if efi.attr&attrDirectory == 0 {
		t.Error("EFI deberia ser un directorio")
	}
	boot := statPath(t, disk, "EFI/boot")
	if boot.attr&attrDirectory == 0 {
		t.Error("EFI/boot deberia ser un directorio")
	}

	// Un hijo directo del raiz lleva 0 en su "..", como manda la especificacion.
	efiData, err := disk.readDir(efi.cluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	efiEntries := parseDir(efiData)
	if len(efiEntries) < 2 {
		t.Fatalf("el directorio nuevo deberia tener . y .., tiene %d entradas", len(efiEntries))
	}
	if efiEntries[0].name != "." || efiEntries[1].name != ".." {
		t.Fatalf("las dos primeras entradas deberian ser . y .., son %q y %q",
			efiEntries[0].name, efiEntries[1].name)
	}
	if efiEntries[0].cluster != efi.cluster {
		t.Errorf("`.` apunta a %d y el directorio es %d", efiEntries[0].cluster, efi.cluster)
	}
	if got := dotCluster(t, efiData[32:64]); got != 0 {
		t.Errorf("`..` de un hijo directo del raiz deberia ser 0, es %d", got)
	}

	// Un nieto apunta a su padre real, no a cero.
	bootData, err := disk.readDir(boot.cluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	if got := dotCluster(t, bootData[32:64]); got != efi.cluster {
		t.Errorf("`..` de EFI/boot apunta a %d, se esperaba %d", got, efi.cluster)
	}

	if err := disk.Mkdir("EFI/deep/inside"); err != nil {
		t.Fatalf("Mkdir anidado: %v", err)
	}
	if err := disk.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk = reload(t, img)
	deep := statPath(t, disk, "EFI/deep/inside")
	deepData, err := disk.readDir(deep.cluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	if got, want := dotCluster(t, deepData[32:64]), statPath(t, disk, "EFI/deep").cluster; got != want {
		t.Errorf("`..` apunta a %d, se esperaba %d", got, want)
	}
}

func TestMkdirIsIdempotent(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := fs.Mkdir("EFI"); err != nil {
			t.Fatalf("Mkdir intento %d: %v", i+1, err)
		}
	}

	data, err := fs.readDir(rootCluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	count := 0
	for _, e := range parseDir(data) {
		if e.attr&attrVolumeID != 0 {
			continue
		}
		count++
	}
	if count != 1 {
		t.Fatalf("el raiz tiene %d entradas y deberia tener una sola", count)
	}
}

func TestWriteFileAndReadBack(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	content := []byte("contenido de prueba para el cargador")
	if err := fs.WriteFile("EFI/boot/bootx64.efi", bytes.NewReader(content)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	got := readPath(t, disk, "EFI/boot/bootx64.efi")
	if !bytes.Equal(got, content) {
		t.Fatalf("leido %q, se esperaba %q", got, content)
	}

	e := statPath(t, disk, "EFI/boot/bootx64.efi")
	if e.size != uint32(len(content)) {
		t.Errorf("tamano en la entrada = %d, se esperaba %d", e.size, len(content))
	}
	if e.attr != attrArchive {
		t.Errorf("atributo = 0x%02X, se esperaba 0x20", e.attr)
	}
	// El nombre conserva las minusculas gracias al LFN.
	if e.name != "bootx64.efi" {
		t.Errorf("nombre = %q", e.name)
	}
}

func TestWriteFileAcrossManyClusters(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	// Con clusters de 512 bytes, 100 KB son unas 200 entradas encadenadas.
	content := make([]byte, 100<<10)
	for i := range content {
		content[i] = byte(i * 7)
	}
	if err := fs.WriteFile("big.bin", bytes.NewReader(content)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	got := readPath(t, disk, "big.bin")
	if !bytes.Equal(got, content) {
		t.Fatalf("los %d bytes no coinciden (leidos %d)", len(content), len(got))
	}

	e := statPath(t, disk, "big.bin")
	chain, err := disk.chainOf(e.cluster)
	if err != nil {
		t.Fatalf("chainOf: %v", err)
	}
	if want := (len(content) + int(disk.geom.clusterSize) - 1) / int(disk.geom.clusterSize); len(chain) != want {
		t.Errorf("la cadena tiene %d clusters, se esperaban %d", len(chain), want)
	}
}

func TestEmptyFileHasNoClusters(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	w, err := fs.Create("vacio.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close del escritor: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	e := statPath(t, disk, "vacio.txt")
	if e.size != 0 {
		t.Errorf("tamano = %d", e.size)
	}
	if e.cluster != 0 {
		t.Errorf("un fichero vacio no deberia tener cluster, tiene %d", e.cluster)
	}
	if got := readPath(t, disk, "vacio.txt"); len(got) != 0 {
		t.Errorf("deberia leerse vacio, tiene %d bytes", len(got))
	}
}

func TestLongNamesSurviveTheRoundTrip(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	names := []string{
		"x86_64-efi",
		"linuxmint-22.3-xfce-64bit.iso",
		"DOSSIER  with spaces",
	}
	for _, n := range names {
		if err := fs.WriteFile(n, strings.NewReader(n)); err != nil {
			t.Fatalf("WriteFile(%q): %v", n, err)
		}
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	for _, n := range names {
		if got := readPath(t, disk, n); string(got) != n {
			t.Errorf("%q: contenido %q", n, got)
		}
		e := statPath(t, disk, n)
		if e.name != n {
			t.Errorf("nombre leido %q, se esperaba %q", e.name, n)
		}
		if e.slots < 2 {
			t.Errorf("%q deberia ocupar varias entradas (LFN + corta), ocupa %d", n, e.slots)
		}
	}
}

func TestShortNameCollisionGetsTilde(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	// Los dos nombres se sanean al mismo 8.3: A_B.TXT.
	if err := fs.WriteFile("a b.txt", strings.NewReader("uno")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.WriteFile("a_b.txt", strings.NewReader("dos")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	data, err := disk.readDir(rootCluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}

	shorts := map[string]string{}
	for _, e := range parseDir(data) {
		if e.attr&attrDirectory != 0 || e.attr&attrVolumeID != 0 {
			continue
		}
		shorts[string(e.short[:])] = e.name
	}
	if len(shorts) != 2 {
		t.Fatalf("hay %d ficheros, se esperaban 2: %v", len(shorts), shorts)
	}

	// El que se sanea lleva tilde; el que ya era 8.3 valido se queda limpio.
	if _, ok := shorts["A_B~1   TXT"]; !ok {
		t.Errorf("\"a b.txt\" deberia acabar en A_B~1.TXT, nombres: %v", shorts)
	}
	if _, ok := shorts["A_B     TXT"]; !ok {
		t.Errorf("\"a_b.txt\" cabe tal cual y no deberia llevar tilde, nombres: %v", shorts)
	}
	if readPath(t, disk, "a b.txt")[0] != 'u' || readPath(t, disk, "a_b.txt")[0] != 'd' {
		t.Error("los contenidos se han cruzado")
	}
}

func TestCreateRefusesDuplicates(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	if err := fs.WriteFile("repetido.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = fs.WriteFile("repetido.txt", strings.NewReader("y"))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("error = %v, se esperaba ErrExists", err)
	}
}

// Un directorio pequeno crece a varios clusters cuando se le acaban los huecos.
func TestDirectoryGrowsWithManyFiles(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	const files = 300
	for i := 0; i < files; i++ {
		name := strings.Repeat("f", 20) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".txt"
		if err := fs.WriteFile("many/"+name, strings.NewReader(name)); err != nil {
			t.Fatalf("WriteFile %d: %v", i, err)
		}
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	dirCluster, found, err := disk.findDir(rootCluster, "many")
	if err != nil || !found {
		t.Fatalf("falta el directorio many: %v", err)
	}
	chain, err := disk.chainOf(dirCluster)
	if err != nil {
		t.Fatalf("chainOf: %v", err)
	}
	if len(chain) < 2 {
		t.Fatalf("el directorio deberia ocupar varios clusters, ocupa %d", len(chain))
	}

	data, err := disk.readDir(dirCluster)
	if err != nil {
		t.Fatalf("readDir: %v", err)
	}
	got := 0
	for _, e := range parseDir(data) {
		if e.attr&attrDirectory != 0 {
			continue
		}
		got++
	}
	if got != files {
		t.Fatalf("%d ficheros en el directorio, se esperaban %d", got, files)
	}
}

// El resumen de clusters libres de la FSInfo tiene que cuadrar con la FAT,
// porque fsck.fat lo comprueba.
func TestFSInfoFreeCountMatchesFAT(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	payload := bytes.Repeat([]byte("x"), 5000)
	if err := fs.WriteFile("uno.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Mkdir("dir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := fs.WriteFile("dir/dos.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	disk := reload(t, img)
	actual := disk.freeClusters()

	infoSector := readBytes(t, img, 512, 512)
	stored := binary.LittleEndian.Uint32(infoSector[488:])
	if stored != actual {
		t.Fatalf("la FSInfo dice %d clusters libres y la FAT tiene %d", stored, actual)
	}

	backup := readBytes(t, img, 7*512, 512)
	if got := binary.LittleEndian.Uint32(backup[488:]); got != stored {
		t.Errorf("el respaldo de la FSInfo dice %d y la principal %d", got, stored)
	}

	if uint64(actual) >= disk.geom.clusters {
		t.Error("despues de escribir deberia haber menos clusters libres que al principio")
	}
}

func TestWriteFileTooBig(t *testing.T) {
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	w, err := fs.Create("enorme.bin")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No hace falta escribir 4 GiB: se comprueba el limite sin tocar el disco.
	w.(*fileWriter).size = maxFileSize - 10
	if _, err := w.Write(make([]byte, 32)); !errors.Is(err, ErrFileTooBig) {
		t.Fatalf("error = %v, se esperaba ErrFileTooBig", err)
	}
}

func TestRunOutOfSpace(t *testing.T) {
	// Un volumen de 40 MiB con 65525 clusters: se llena con un solo fichero
	// grande y el siguiente no cabe.
	img := newImage(t, smallVolume)
	fs, err := Format(img, Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	big := make([]byte, 40<<20)
	err = fs.WriteFile("llena.bin", bytes.NewReader(big))
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("error = %v, se esperaba ErrNoSpace", err)
	}
}

func dotCluster(t *testing.T, entry []byte) uint32 {
	t.Helper()
	return uint32(binary.LittleEndian.Uint16(entry[26:])) | uint32(binary.LittleEndian.Uint16(entry[20:]))<<16
}
