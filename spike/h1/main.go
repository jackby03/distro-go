// Spike H-1: valida el contrato rootfs.Source/Target contra una ISO real,
// mide el coste de materializar el árbol en un ext4 de go-diskfs y genera un
// .img para verificarlo con e2fsck en QEMU.
//
// No es código de producto: vive en spike/ y se absorbe en internal/ tras la
// revisión.
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CalebQ42/squashfs"
	squashfslow "github.com/CalebQ42/squashfs/low"
	"github.com/diskfs/go-diskfs/backend"
	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/kdomanski/iso9660"

	"distrogo/pkg/rootfs"
)

var errStop = errors.New("límite del spike alcanzado")

func main() {
	isoPath := flag.String("iso", `C:\Users\jackd\Downloads\linuxmint-22.3-xfce-64bit.iso`, "ISO de origen")
	imgPath := flag.String("img", `out\spike-h1-root.img`, "imagen ext4 de salida")
	sizeGiB := flag.Int64("size", 12, "tamaño de la imagen ext4 (GiB)")
	reportPath := flag.String("report", `out\spike-h1-report.json`, "informe JSON")
	maxFiles := flag.Int64("max-files", 0, "máximo de entradas a procesar (0 = todas)")
	maxBytes := flag.Int64("max-bytes", 0, "máximo de bytes de contenido (0 = todos)")
	noWrite := flag.Bool("nowrite", false, "sólo recorrer y medir (no crea imagen)")
	flag.Parse()

	if err := run(*isoPath, *imgPath, *sizeGiB, *reportPath, *maxFiles, *maxBytes, *noWrite); err != nil {
		fmt.Fprintln(os.Stderr, "\nERROR:", err)
		os.Exit(1)
	}
}

func run(isoPath, imgPath string, sizeGiB int64, reportPath string, maxFiles, maxBytes int64, noWrite bool) error {
	rep := &report{ISO: isoPath, StartedAt: time.Now().Format(time.RFC3339)}

	// ---------------------------------------------------------------- ISO
	isoFile, err := os.Open(isoPath)
	if err != nil {
		return fmt.Errorf("abrir ISO: %w", err)
	}
	defer isoFile.Close()

	img, err := iso9660.OpenImage(isoFile)
	if err != nil {
		return fmt.Errorf("parsear ISO9660: %w", err)
	}
	rootDir, err := img.RootDir()
	if err != nil {
		return fmt.Errorf("root dir de la ISO: %w", err)
	}
	if label, err := img.Label(); err == nil {
		rep.ISOLabel = label
	}

	fmt.Println("== ISO ==")
	fmt.Printf("  fichero : %s\n", isoPath)
	if st, err := isoFile.Stat(); err == nil {
		rep.ISOSize = st.Size()
		fmt.Printf("  tamaño  : %.2f GiB\n", float64(st.Size())/(1<<30))
	}
	fmt.Printf("  etiqueta: %s\n", rep.ISOLabel)

	// ficheros live (para H2) y el squashfs
	for _, p := range []string{"casper/vmlinuz", "casper/initrd.lz", "casper/filesystem.size", "casper/filesystem.manifest"} {
		if f, err := isoFind(rootDir, p); err == nil {
			rep.ISOFiles = append(rep.ISOFiles, isoFileFacts{Path: p, Size: f.Size()})
		}
	}
	sqFile, err := isoFind(rootDir, "casper/filesystem.squashfs")
	if err != nil {
		return fmt.Errorf("localizar squashfs: %w", err)
	}

	sr, ok := sqFile.Reader().(*io.SectionReader)
	if !ok {
		return fmt.Errorf("iso9660.File.Reader() no devolvió *io.SectionReader")
	}
	rep.Squashfs.Size = sr.Size()

	// ------------------------------------------------------------ SquashFS
	sq, err := squashfs.NewReader(sr)
	if err != nil {
		return fmt.Errorf("abrir squashfs: %w", err)
	}
	sb := sq.Low.Superblock
	rep.Squashfs.Compression = compressionName(sb.CompType)
	rep.Squashfs.BlockSize = sb.BlockSize
	rep.Squashfs.BlockLog = sb.BlockLog
	rep.Squashfs.SuperblockInodes = sb.InodeCount
	rep.Squashfs.Fragments = sb.FragCount
	rep.Squashfs.IDCount = sb.IdCount
	rep.Squashfs.Flags = sb.Flags
	rep.Squashfs.BytesUsed = sb.Size
	rep.Squashfs.Version = fmt.Sprintf("%d.%d", sb.VerMaj, sb.VerMin)

	fmt.Println("\n== SquashFS ==")
	fmt.Printf("  versión   : %s\n", rep.Squashfs.Version)
	fmt.Printf("  compresión: %s (bloque %d KiB)\n", rep.Squashfs.Compression, sb.BlockSize/1024)
	fmt.Printf("  inodos    : %d · fragmentos: %d · ids: %d\n", sb.InodeCount, sb.FragCount, sb.IdCount)
	fmt.Printf("  flags     : 0x%04X%s\n", sb.Flags, flagNotes(sb.Flags))

	src := newSquashSource(&sq)

	// ------------------------------------------------------- destino ext4
	var tgt rootfs.Target
	var bstore backend.Storage
	var pop *populateFacts
	var efs *ext4.FileSystem

	if !noWrite {
		if err := os.MkdirAll(path.Dir(imgPath), 0o755); err != nil {
			return fmt.Errorf("crear dir de salida: %w", err)
		}
		size := sizeGiB << 30
		if err := os.Remove(imgPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("borrar imagen previa: %w", err)
		}
		bstore, err = file.CreateFromPath(imgPath, size)
		if err != nil {
			return fmt.Errorf("crear %s: %w", imgPath, err)
		}
		defer bstore.Close()

		inodes := uint32(800_000)
		if maxFiles > 0 && maxFiles*2 < 800_000 {
			inodes = uint32(maxFiles * 2)
		}
		params := &ext4.Params{
			VolumeName: "distrogo-root",
			InodeCount: inodes,
			Features: []ext4.FeatureOpt{
				ext4.WithFeatureHasJournal(false),
			},
		}
		t0 := time.Now()
		efs, err = ext4.Create(bstore, size, 0, 512, params)
		if err != nil {
			return fmt.Errorf("ext4.Create: %w", err)
		}
		fmt.Printf("\n== ext4 ==")
		fmt.Printf("\n  imagen : %s (%d GiB, %d inodos)\n", imgPath, sizeGiB, inodes)
		fmt.Printf("  creado : %s\n", time.Since(t0).Round(time.Millisecond))

		tgt = &ext4Target{fs: efs}
		pop = &populateFacts{Ops: map[string]*opStat{}}
	}

	// ------------------------------------------------------- walk + write
	col := newCollector(pop)
	var dirs []rootfs.Entry
	inodeFirstPath := map[uint64]string{}
	hardlinksUnsupported := false
	buf := make([]byte, 1<<20)
	remaining := maxBytes
	if remaining == 0 {
		remaining = -1
	}

	tWalk := time.Now()
	err = src.Walk(func(e rootfs.Entry) error {
		if maxFiles > 0 && col.facts.Entries >= maxFiles {
			return errStop
		}
		col.observe(e)

		if pop == nil {
			return nil
		}

		switch e.Type {
		case rootfs.TypeDir:
			col.timed(pop, "mkdir", func() error { return tgt.Mkdir(e) })
			col.timed(pop, "chmod", func() error { return tgt.Chmod(e) })
			col.timed(pop, "chown", func() error { return tgt.Chown(e) })
			pop.Dirs++
			dirs = append(dirs, e)

		case rootfs.TypeRegular:
			// hardlink: el contenido se escribió con la primera aparición
			if e.Nlink > 1 {
				if first, ok := inodeFirstPath[e.Inode]; ok {
					if hardlinksUnsupported {
						pop.HardlinksSkipped++
						return nil
					}
					err := col.timed(pop, "hardlink", func() error {
						return tgt.Hardlink(rootfs.Entry{Path: e.Path, Link: first})
					})
					if errors.Is(err, filesystem.ErrNotImplemented) || errors.Is(err, filesystem.ErrNotSupported) {
						hardlinksUnsupported = true
						pop.HardlinksSkipped++
					} else if err == nil {
						pop.Hardlinks++
					}
					return nil
				}
				inodeFirstPath[e.Inode] = e.Path
			}

			var w io.WriteCloser
			if err := col.timed(pop, "create", func() error {
				var e2 error
				w, e2 = tgt.Create(e)
				return e2
			}); err != nil {
				return nil
			}

			if e.Size == 0 {
				// la librería no puede abrir ficheros de 0 bytes ("invalid block
				// index"): su contenido es vacío, así que basta con crearlo.
				col.timed(pop, "close", func() error { return w.Close() })
				pop.Files++
			} else {
				rc, err := src.Open(e.Path)
				if err != nil {
					_ = w.Close()
					col.rec(pop, "open", err, 0)
					return nil
				}
				var n int64
				cerr := col.timed(pop, "write", func() error {
					var cerr error
					n, cerr = copyLimited(w, rc, buf, &remaining)
					return cerr
				})
				_ = rc.Close()
				cerr2 := col.timed(pop, "close", func() error { return w.Close() })
				pop.Bytes += n
				if cerr2 != nil && cerr == nil {
					cerr = cerr2
				}
				if errors.Is(cerr, errStop) {
					pop.Files++
					pop.Stopped = true
					return errStop
				}
				if cerr != nil {
					return nil
				}
				pop.Files++
			}
			col.timed(pop, "chmod", func() error { return tgt.Chmod(e) })
			col.timed(pop, "chown", func() error { return tgt.Chown(e) })

		case rootfs.TypeSymlink:
			if err := col.timed(pop, "symlink", func() error { return tgt.Symlink(e) }); err == nil {
				pop.Symlinks++
			}

		default: // device / fifo / socket
			if err := col.timed(pop, "mknod", func() error { return tgt.Mknod(e) }); err == nil {
				pop.Devices++
			}
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
		pop.Stopped = true
	}
	col.facts.DurMS = time.Since(tWalk).Milliseconds()
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	// mtime de directorios: post-orden inverso (los hijos ya no cambian)
	if pop != nil {
		for i := len(dirs) - 1; i >= 0; i-- {
			d := dirs[i]
			col.timed(pop, "settimes", func() error { return tgt.SetTimes(d) })
		}
		for _, st := range pop.Ops {
			if n := st.OK + st.NotImpl + st.Err; n > 0 {
				st.AvgUS = st.TotalMS * 1000 / n
			}
		}
		pop.DurMS = time.Since(tWalk).Milliseconds()
		pop.MBps = mbps(pop.Bytes, pop.DurMS)
	}

	col.finalize()

	if efs != nil && pop != nil {
		rep.Verify = verifyRoundTrip(efs, src, col)
	}

	// ------------------------------------------------------------- informe
	if efs != nil {
		if err := efs.Close(); err != nil {
			fmt.Println("  aviso: ext4.Close:", err)
		}
	}
	if bstore != nil {
		if err := bstore.Close(); err != nil {
			fmt.Println("  aviso: close imagen:", err)
		}
	}

	rep.Walk = col.facts
	rep.Populate = pop
	rep.FinishedAt = time.Now().Format(time.RFC3339)

	printHuman(rep)

	if reportPath != "" {
		if err := os.MkdirAll(path.Dir(reportPath), 0o755); err != nil {
			return err
		}
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(reportPath, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("\ninforme escrito en %s\n", reportPath)
	}
	return nil
}

// ---------------------------------------------------------------- helpers

func isoFind(root *iso9660.File, p string) (*iso9660.File, error) {
	cur := root
	for _, comp := range strings.Split(p, "/") {
		kids, err := cur.GetChildren()
		if err != nil {
			return nil, err
		}
		var next *iso9660.File
		for _, k := range kids {
			if strings.EqualFold(k.Name(), comp) {
				next = k
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("no existe %q en la ISO", p)
		}
		cur = next
	}
	return cur, nil
}

func copyLimited(dst io.Writer, src io.Reader, buf []byte, remaining *int64) (int64, error) {
	var n int64
	for {
		if *remaining == 0 {
			return n, errStop
		}
		chunk := int64(len(buf))
		if *remaining > 0 && chunk > *remaining {
			chunk = *remaining
		}
		r, rerr := src.Read(buf[:chunk])
		if r > 0 {
			w, werr := dst.Write(buf[:r])
			n += int64(w)
			if *remaining > 0 {
				*remaining -= int64(w)
			}
			if werr != nil {
				return n, werr
			}
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			return n, rerr
		}
	}
}

func compressionName(c uint16) string {
	switch c {
	case squashfslow.ZlibCompression:
		return "gzip"
	case squashfslow.LZMACompression:
		return "lzma"
	case squashfslow.LZOCompression:
		return "lzo"
	case squashfslow.XZCompression:
		return "xz"
	case squashfslow.LZ4Compression:
		return "lz4"
	case squashfslow.ZSTDCompression:
		return "zstd"
	default:
		return fmt.Sprintf("desconocida(%d)", c)
	}
}

func flagNotes(flags uint16) string {
	names := []struct {
		bit  uint16
		name string
	}{
		{0x0001, "uncompressed-inodes"},
		{0x0002, "uncompressed-data"},
		{0x0004, "check"},
		{0x0008, "uncompressed-fragments"},
		{0x0010, "no-fragments"},
		{0x0020, "always-fragments"},
		{0x0040, "duplicates"},
		{0x0080, "exportable"},
		{0x0100, "uncompressed-xattrs"},
		{0x0200, "xattrs"},
		{0x0400, "compressor-options"},
	}
	var out []string
	for _, n := range names {
		if flags&n.bit != 0 {
			out = append(out, n.name)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return " [" + strings.Join(out, ", ") + "]"
}

func mbps(bytes int64, ms int64) float64 {
	if ms <= 0 {
		return 0
	}
	return float64(bytes) / (1 << 20) / (float64(ms) / 1000)
}

// ------------------------------------------------------- collector/report

type isoFileFacts struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type squashFacts struct {
	Compression      string `json:"compression"`
	BlockSize        uint32 `json:"block_size"`
	BlockLog         uint16 `json:"block_log"`
	SuperblockInodes uint32 `json:"superblock_inodes"`
	Fragments        uint32 `json:"fragments"`
	IDCount          uint16 `json:"id_count"`
	Flags            uint16 `json:"flags"`
	BytesUsed        uint64 `json:"bytes_used"`
	Version          string `json:"version"`
	Size             int64  `json:"section_size"`
}

type hardlinkFacts struct {
	Entries int64    `json:"entries"`
	Inodes  int64    `json:"inodes"`
	Samples []string `json:"samples,omitempty"`
}

type deviceFacts struct {
	Path  string `json:"path"`
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
	Mode  string `json:"mode"`
}

type walkFacts struct {
	Entries      int64            `json:"entries"`
	ByType       map[string]int64 `json:"by_type"`
	ContentBytes int64            `json:"content_bytes"`
	MaxSize      int64            `json:"max_file_size"`
	MaxSizePath  string           `json:"max_file_path"`
	Setuid       int64            `json:"setuid_files"`
	Setgid       int64            `json:"setgid_files"`
	NlinkGT1     int64            `json:"nlink_gt1"`
	UIDs         []uint32         `json:"uids"`
	GIDs         []uint32         `json:"gids"`
	UIDCounts    map[uint32]int64 `json:"uid_counts"`
	GIDCounts    map[uint32]int64 `json:"gid_counts"`
	Hardlinks    hardlinkFacts    `json:"hardlinks"`
	Devices      []deviceFacts    `json:"devices,omitempty"`
	MaxDepth     int              `json:"max_depth"`
	MaxPathLen   int              `json:"max_path_len"`
	MaxPath      string           `json:"max_path"`
	DurMS        int64            `json:"walk_ms"`
}

type opStat struct {
	OK       int64  `json:"ok"`
	NotImpl  int64  `json:"not_implemented"`
	Err      int64  `json:"error"`
	TotalMS  int64  `json:"total_ms"`
	AvgUS    int64  `json:"avg_us"`
	FirstErr string `json:"first_error,omitempty"`
}

type populateFacts struct {
	Files            int64              `json:"files_written"`
	Dirs             int64              `json:"dirs_written"`
	Symlinks         int64              `json:"symlinks_written"`
	Hardlinks        int64              `json:"hardlinks_written"`
	HardlinksSkipped int64              `json:"hardlinks_skipped"`
	Devices          int64              `json:"devices_written"`
	Bytes            int64              `json:"bytes_written"`
	DurMS            int64              `json:"duration_ms"`
	MBps             float64            `json:"mb_per_s"`
	Stopped          bool               `json:"stopped_by_limit"`
	Ops              map[string]*opStat `json:"ops"`
}

type report struct {
	ISO        string         `json:"iso"`
	ISOLabel   string         `json:"iso_label"`
	ISOSize    int64          `json:"iso_size"`
	ISOFiles   []isoFileFacts `json:"iso_files,omitempty"`
	Squashfs   squashFacts    `json:"squashfs"`
	Walk       walkFacts      `json:"walk"`
	Populate   *populateFacts `json:"populate,omitempty"`
	Verify     *verifyFacts   `json:"verify,omitempty"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
}

type collector struct {
	facts          walkFacts
	uidSet, gidSet map[uint32]struct{}
	inodeNlink     map[uint64]uint32
	hlInodes       map[uint64]struct{}
	pop            *populateFacts

	// candidatos para la verificación de ida y vuelta
	fileSamples []rootfs.Entry
	linkSamples []rootfs.Entry
	modeSamples []rootfs.Entry
}

func newCollector(pop *populateFacts) *collector {
	return &collector{
		facts: walkFacts{
			ByType:    map[string]int64{},
			UIDCounts: map[uint32]int64{},
			GIDCounts: map[uint32]int64{},
		},
		uidSet:     map[uint32]struct{}{},
		gidSet:     map[uint32]struct{}{},
		inodeNlink: map[uint64]uint32{},
		hlInodes:   map[uint64]struct{}{},
		pop:        pop,
	}
}

func (c *collector) observe(e rootfs.Entry) {
	f := &c.facts
	f.Entries++
	f.ByType[e.Type.String()]++
	f.UIDCounts[e.UID]++
	f.GIDCounts[e.GID]++
	c.uidSet[e.UID] = struct{}{}
	c.gidSet[e.GID] = struct{}{}

	if depth := strings.Count(e.Path, "/"); depth > f.MaxDepth {
		f.MaxDepth = depth
	}
	if len(e.Path) > f.MaxPathLen {
		f.MaxPathLen, f.MaxPath = len(e.Path), e.Path
	}
	if e.Mode&os.ModeSetuid != 0 {
		f.Setuid++
	}
	if e.Mode&os.ModeSetgid != 0 {
		f.Setgid++
	}
	if e.Nlink > 1 {
		f.NlinkGT1++
		c.inodeNlink[e.Inode] = e.Nlink
		if _, dup := c.hlInodes[e.Inode]; !dup {
			c.hlInodes[e.Inode] = struct{}{}
			f.Hardlinks.Entries++
			if len(f.Hardlinks.Samples) < 10 {
				f.Hardlinks.Samples = append(f.Hardlinks.Samples, fmt.Sprintf("%s (inodo %d, nlink %d)", e.Path, e.Inode, e.Nlink))
			}
		}
	}
	if e.Type == rootfs.TypeDevice && len(f.Devices) < 40 {
		f.Devices = append(f.Devices, deviceFacts{
			Path:  e.Path,
			Major: e.Major(),
			Minor: e.Minor(),
			Mode:  e.Mode.String(),
		})
	}
	if e.Type == rootfs.TypeRegular {
		f.ContentBytes += e.Size
		if e.Size > f.MaxSize {
			f.MaxSize, f.MaxSizePath = e.Size, e.Path
		}
		if len(c.fileSamples) < 6 && e.Size > 1024 && e.Size < 64<<20 {
			c.fileSamples = append(c.fileSamples, e)
		}
	}
	if e.Type == rootfs.TypeSymlink && len(c.linkSamples) < 6 {
		c.linkSamples = append(c.linkSamples, e)
	}
	if e.Type != rootfs.TypeDir && len(c.modeSamples) < 8 {
		if len(c.modeSamples) < 4 || e.Mode&os.ModeSetuid != 0 {
			c.modeSamples = append(c.modeSamples, e)
		}
	}
}

func (c *collector) finalize() {
	f := &c.facts
	f.Hardlinks.Inodes = int64(len(c.hlInodes))
	for u := range c.uidSet {
		f.UIDs = append(f.UIDs, u)
	}
	for g := range c.gidSet {
		f.GIDs = append(f.GIDs, g)
	}
	sort.Slice(f.UIDs, func(i, j int) bool { return f.UIDs[i] < f.UIDs[j] })
	sort.Slice(f.GIDs, func(i, j int) bool { return f.GIDs[i] < f.GIDs[j] })
}

func (c *collector) timed(pop *populateFacts, op string, fn func() error) error {
	t0 := time.Now()
	err := fn()
	c.rec(pop, op, err, time.Since(t0))
	return err
}

func (c *collector) rec(pop *populateFacts, op string, err error, dur time.Duration) {
	st := pop.Ops[op]
	if st == nil {
		st = &opStat{}
		pop.Ops[op] = st
	}
	st.TotalMS += dur.Milliseconds()
	switch {
	case err == nil:
		st.OK++
	case errors.Is(err, filesystem.ErrNotImplemented), errors.Is(err, filesystem.ErrNotSupported):
		st.NotImpl++
		if st.FirstErr == "" {
			st.FirstErr = err.Error()
		}
	default:
		st.Err++
		if st.FirstErr == "" {
			st.FirstErr = err.Error()
		}
	}
}

func printHuman(r *report) {
	f := r.Walk
	fmt.Println("\n== walk (metadatos) ==")
	fmt.Printf("  entradas: %d en %d ms\n", f.Entries, f.DurMS)
	keys := make([]string, 0, len(f.ByType))
	for k := range f.ByType {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("    %-8s %d\n", k, f.ByType[k])
	}
	fmt.Printf("  contenido total : %.2f GiB\n", float64(f.ContentBytes)/(1<<30))
	fmt.Printf("  fichero máximo  : %s (%.1f MiB)\n", f.MaxSizePath, float64(f.MaxSize)/(1<<20))
	fmt.Printf("  setuid/setgid   : %d / %d\n", f.Setuid, f.Setgid)
	fmt.Printf("  uids            : %d distintos %v\n", len(f.UIDs), f.UIDs)
	fmt.Printf("  gids            : %d distintos %v\n", len(f.GIDs), f.GIDs)
	fmt.Printf("  hardlinks       : %d inodos con nlink>1 (%d entradas)\n", f.Hardlinks.Inodes, f.NlinkGT1)
	fmt.Printf("  device nodes    : %d\n", len(f.Devices))
	fmt.Printf("  ruta más larga  : %d bytes, profundidad %d\n", f.MaxPathLen, f.MaxDepth)
	if f.MaxPathLen > 0 {
		fmt.Printf("    %s\n", f.MaxPath)
	}

	if p := r.Populate; p != nil {
		fmt.Println("\n== populate (ext4) ==")
		fmt.Printf("  ficheros %d · dirs %d · symlinks %d · hardlinks %d (saltados %d) · dev %d\n",
			p.Files, p.Dirs, p.Symlinks, p.Hardlinks, p.HardlinksSkipped, p.Devices)
		fmt.Printf("  escritos %.2f GiB en %d ms (%.1f MiB/s)\n",
			float64(p.Bytes)/(1<<30), p.DurMS, p.MBps)
		if p.Stopped {
			fmt.Println("  (detenido por límite: cifras parciales)")
		}
		ops := make([]string, 0, len(p.Ops))
		for k := range p.Ops {
			ops = append(ops, k)
		}
		sort.Strings(ops)
		fmt.Println("  matriz de operaciones:")
		for _, k := range ops {
			st := p.Ops[k]
			line := fmt.Sprintf("    %-9s ok=%-7d notimpl=%-4d err=%-5d total=%-7dms avg=%dµs", k, st.OK, st.NotImpl, st.Err, st.TotalMS, st.AvgUS)
			if st.FirstErr != "" {
				line += "  «" + st.FirstErr + "»"
			}
			fmt.Println(line)
		}
	}

	if v := r.Verify; v != nil {
		fmt.Println("\n== verify (ida y vuelta, sin QEMU) ==")
		fmt.Printf("  contenido : %d/%d md5 OK\n", v.FilesOK, v.FilesChecked)
		fmt.Printf("  symlinks  : %d/%d OK\n", v.SymlinksOK, v.SymlinksChecked)
		fmt.Printf("  modo      : %d/%d OK\n", v.ModesOK, v.ModesChecked)
		for _, m := range v.FileMismatch {
			fmt.Println("    MISMATCH", m)
		}
		for _, m := range v.SymlinkMismatch {
			fmt.Println("    MISMATCH", m)
		}
		for _, m := range v.ModeMismatch {
			fmt.Println("    MISMATCH", m)
		}
		for _, m := range v.Errors {
			fmt.Println("    ERROR", m)
		}
	}
}

// ------------------------------- verificación de ida y vuelta (sin QEMU)

type verifyFacts struct {
	FilesChecked    int      `json:"files_checked"`
	FilesOK         int      `json:"files_ok"`
	FileMismatch    []string `json:"file_mismatches,omitempty"`
	SymlinksChecked int      `json:"symlinks_checked"`
	SymlinksOK      int      `json:"symlinks_ok"`
	SymlinkMismatch []string `json:"symlink_mismatches,omitempty"`
	ModesChecked    int      `json:"modes_checked"`
	ModesOK         int      `json:"modes_ok"`
	ModeMismatch    []string `json:"mode_mismatches,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

func verifyRoundTrip(efs *ext4.FileSystem, src rootfs.Source, c *collector) *verifyFacts {
	v := &verifyFacts{}

	for _, e := range c.fileSamples {
		v.FilesChecked++
		want, err1 := md5OfSource(src, e.Path)
		got, err2 := md5OfImage(efs, e.Path)
		if err1 != nil || err2 != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("%s: origen=%v imagen=%v", e.Path, err1, err2))
			continue
		}
		if want == got {
			v.FilesOK++
		} else {
			v.FileMismatch = append(v.FileMismatch, fmt.Sprintf("%s (origen %s imagen %s)", e.Path, want, got))
		}
	}

	for _, e := range c.linkSamples {
		v.SymlinksChecked++
		got, err := efs.ReadLink(e.Path)
		if err != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("readlink %s: %v", e.Path, err))
			continue
		}
		if got == e.Link {
			v.SymlinksOK++
		} else {
			v.SymlinkMismatch = append(v.SymlinkMismatch, fmt.Sprintf("%s: se esperaba %q, hay %q", e.Path, e.Link, got))
		}
	}

	for _, e := range c.modeSamples {
		v.ModesChecked++
		fi, err := efs.Stat(e.Path)
		if err != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("stat %s: %v", e.Path, err))
			continue
		}
		wantSU := e.Mode&os.ModeSetuid != 0
		gotSU := fi.Mode()&os.ModeSetuid != 0
		if fi.Mode().Perm() == e.Mode.Perm() && wantSU == gotSU {
			v.ModesOK++
		} else {
			v.ModeMismatch = append(v.ModeMismatch, fmt.Sprintf("%s: se esperaba %v, hay %v", e.Path, e.Mode, fi.Mode()))
		}
	}
	return v
}

func md5OfSource(src rootfs.Source, p string) (string, error) {
	rc, err := src.Open(p)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	h := md5.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func md5OfImage(efs *ext4.FileSystem, p string) (string, error) {
	f, err := efs.OpenFile(p, os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
