package cli

import (
	"fmt"
	"time"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/efilayout"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
	"distrogo/pkg/gpt"
	"distrogo/pkg/linuxiso"
	"distrogo/pkg/rootfs"
	"distrogo/pkg/squashfs"
)

// partitionOptions son las decisiones de una instalacion, ya resueltas por la CLI.
//
// Existe para que la tuberia no dependa de las flags: la imagen y el disco fisico
// la llaman con los mismos datos, y asi lo que se prueba en un .img es lo mismo
// que se escribe en hardware.
type partitionOptions struct {
	espSize      int64
	formatESP    bool
	formatRoot   bool
	populate     bool
	install      bool
	bootFrom     string
	withSquashfs bool
	kernelArgs   string
	title        string
	// fallbackTitle se usa si no se puede leer PRETTY_NAME del sistema instalado.
	fallbackTitle string
	userName      string
	userUID       int
	passwordHash  string
}

// partitionResult es lo que produce la tuberia para el resumen final.
type partitionResult struct {
	table       *gpt.Table
	layout      gpt.Layout
	espInfo     *fat32.Info
	espContent  *efilayout.Result
	rootInfo    *ext4fs.Info
	rootStats   *rootfs.Stats
	accountInfo *userResult
}

// applyPartition ejecuta la instalacion completa sobre un dispositivo abierto.
//
// Quien llama decide que dispositivo abrir y con que comprobaciones de seguridad;
// aqui solo se hace el trabajo, en este orden: tabla GPT, ESP, root ext4, volcado,
// arranque y cuenta. Es el mismo codigo para una imagen y para un disco fisico.
func (a *App) applyPartition(dev blockdev.Writable, layout gpt.Layout, opts partitionOptions) (*partitionResult, error) {
	res := &partitionResult{layout: layout}

	entries, err := gpt.Plan(layout, []gpt.SizeSpec{
		{Name: "ESP", Type: gpt.TypeESP, SizeBytes: opts.espSize},
		{Name: "root", Type: gpt.TypeLinuxFilesystem},
	})
	if err != nil {
		return res, fmt.Errorf("repartir: %w", err)
	}

	table, err := gpt.NewTable(gpt.GUID{}, entries)
	if err != nil {
		return res, err
	}

	if err := gpt.Commit(dev, table, layout); err != nil {
		return res, fmt.Errorf("escribir la tabla GPT: %w", err)
	}

	// Verificacion con nuestro propio lector: si algo no cuadra, mejor saberlo
	// aqui que en el invitado de QEMU.
	back, _, err := gpt.Read(dev)
	if err != nil {
		return res, fmt.Errorf("verificar la tabla escrita: %w", err)
	}
	if err := sameTable(table, back); err != nil {
		return res, fmt.Errorf("la tabla releida no coincide: %w", err)
	}
	res.table = table

	// La ESP es una particion, asi que se formatea a traves de una vista recortada
	// del dispositivo. Es el mismo camino que el root.
	var espVol *fat32.FileSystem
	var rootVol *ext4fs.FS

	if opts.formatESP {
		esp := table.Partitions[0]
		offset := int64(esp.FirstLBA) * int64(layout.SectorSize)
		a.phase("formateando la ESP (%s)", blockdev.HumanSize(esp.Bytes(layout.SectorSize)))

		section, err := blockdev.NewSection(dev, offset, esp.Bytes(layout.SectorSize))
		if err != nil {
			return res, fmt.Errorf("preparar la ESP: %w", err)
		}

		vol, err := fat32.Format(section, fat32.Options{Label: "SYSTEM"})
		if err != nil {
			return res, fmt.Errorf("formatear la ESP: %w", err)
		}
		info := vol.Info()
		res.espInfo = &info
		espVol = vol

		if opts.bootFrom != "" {
			content, err := a.populateESP(vol, opts.bootFrom, opts.withSquashfs, opts.kernelArgs, opts.install)
			if err != nil {
				return res, err
			}
			res.espContent = &content
		}

		// El volumen se cierra al final: una instalacion real todavia tiene que
		// copiarle el kernel y el initrd.
		if !opts.install {
			if err := vol.Close(); err != nil {
				return res, fmt.Errorf("cerrar la ESP: %w", err)
			}
			espVol = nil
		}
	}

	if opts.formatRoot {
		root := table.Partitions[1]
		offset := int64(root.FirstLBA) * int64(layout.SectorSize)
		rootSize := root.Bytes(layout.SectorSize)

		// Si se va a volcar el arbol, el numero de inodos sale del squashfs: la
		// ratio por defecto se queda corta con un rootfs de distro real.
		var inodes uint32
		var src *squashfs.FS
		if opts.populate {
			var err error
			var srcISO *linuxiso.ISO
			src, srcISO, err = openSquashfs(opts.bootFrom)
			if err != nil {
				return res, err
			}
			// El squashfs lee del ISO: hay que mantenerlo abierto hasta el final.
			defer func() { _ = srcISO.Close() }()

			info := src.Info()
			// Un 10 por ciento de margen para lo que el arbol no cuenta: lost+found,
			// ficheros generados al primer arranque y demas.
			inodes = info.Inodes + info.Inodes/10 + 1024
			fmt.Fprintf(a.Err, "rootfs     : %d inodos en el squashfs, se reservan %d\n", info.Inodes, inodes)
		}

		vol, err := ext4fs.Format(dev, offset, rootSize, ext4fs.Options{
			Label:      "distrogo-root",
			InodeCount: inodes,
		})
		if err != nil {
			return res, fmt.Errorf("formatear el root: %w", err)
		}
		rootVol = vol

		if opts.populate {
			a.phase("volcando el rootfs del ISO en la particion root")
			started := time.Now()
			expected := int64(src.Info().Inodes)

			stats, err := rootfs.Populate(src, vol.Target(), rootfs.PopulateOptions{
				TolerateUnsupported: true,
				Progress:            a.progressFunc(started, expected),
			})
			fmt.Fprintln(a.Err)
			if err != nil {
				return res, fmt.Errorf("volcar el rootfs: %w", err)
			}
			res.rootStats = &stats
			a.phase("rootfs volcado: %d dirs, %d ficheros, %d symlinks, %s en %s",
				stats.Dirs, stats.Files, stats.Symlinks,
				blockdev.HumanSize(stats.Bytes), time.Since(started).Round(time.Second))
		}

		// El UUID del root hace falta para el grub.cfg y el fstab.
		rootNow, err := vol.Info()
		if err != nil {
			return res, fmt.Errorf("leer el root: %w", err)
		}
		a.phase("root formateado: ext4, %d inodos, UUID %s", rootNow.InodeCount, rootNow.UUID)

		if opts.install {
			if espVol == nil {
				return res, fmt.Errorf("--install necesita la ESP formateada")
			}

			a.phase("instalando el arranque en el ESP")
			label := opts.title
			if label == "" {
				label = defaultTitle(rootVol, opts.fallbackTitle)
			}
			content, err := a.installTarget(espVol, rootVol, *res.espInfo, rootNow, label)
			if err != nil {
				return res, fmt.Errorf("instalar el arranque: %w", err)
			}
			res.espContent = &content

			if opts.userName != "" {
				account, err := a.buildAccount(opts.userName, opts.userUID, opts.passwordHash)
				if err != nil {
					return res, fmt.Errorf("preparar la cuenta: %w", err)
				}

				a.phase("creando la cuenta %s en el sistema instalado", account.Name)
				created, err := a.createUser(rootVol, account)
				if err != nil {
					return res, fmt.Errorf("crear la cuenta: %w", err)
				}
				res.accountInfo = &created
			}
		}

		info, err := vol.Info()
		if err != nil {
			return res, fmt.Errorf("leer el root: %w", err)
		}
		if err := vol.Close(); err != nil {
			return res, fmt.Errorf("cerrar el root: %w", err)
		}
		rootVol = nil
		res.rootInfo = &info
	}

	if espVol != nil {
		if err := espVol.Close(); err != nil {
			return res, fmt.Errorf("cerrar la ESP: %w", err)
		}
	}

	return res, nil
}

// progressFunc devuelve el callback que pinta el avance del volcado, con ritmo y
// tiempo restante. Se emite por tiempo y no por numero de entradas: en un volcado
// de 45 minutos, esperar a la entrada 20.000 deja la pantalla muda demasiado rato.
func (a *App) progressFunc(started time.Time, expected int64) func(rootfs.Stats, string) {
	return func(st rootfs.Stats, current string) {
		done := st.Dirs + st.Files + st.Symlinks + st.Devices
		elapsed := time.Since(started)

		rate := 0.0
		if elapsed > 0 {
			rate = float64(done) / elapsed.Seconds()
		}
		eta := "-"
		if rate > 0 && expected > done {
			eta = time.Duration(float64(expected-done) / rate * float64(time.Second)).Round(time.Second).String()
		}

		fmt.Fprintf(a.Err, "\r[%s] %d/%d entradas · %.0f/s · quedan %s · %s        ",
			time.Now().Format("15:04:05"), done, expected, rate, eta, current)
	}
}
