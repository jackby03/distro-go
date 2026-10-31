//go:build windows

package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/gpt"
	"distrogo/pkg/safety"
	"distrogo/pkg/windisk"
)

// flashChunk es cuanto se lee de la imagen y se escribe al disco de una vez.
//
// Grande, para que el disco trabaje en secuencia y no en saltos, y pequeno para no
// cargar la imagen entera en memoria. 4 MiB es multiplo del sector, asi que entra
// por el camino rapido del disco.
const flashChunk = 4 << 20

// runFlash copia una imagen ya construida a un disco fisico, de forma secuencial.
//
// Es la via buena para hardware: escribir un fichero de principio a fin permite al
// disco y al USB trabajar en secuencia, mientras que construir el sistema de
// ficheros sobre el disco hace decenas de accesos aleatorios por cada entrada y en
// un HDD eso son horas.
func (a *App) runFlash(args []string) int {
	fs := flag.NewFlagSet("flash", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	imagePath := fs.String("image", "", "imagen ya construida con partition (obligatorio)")
	target := fs.Int("target-disk", -1, "numero de disco fisico, el que muestra list")
	expectSerial := fs.String("expect-serial", "", "serial del disco; si no coincide no se toca nada")
	allowNonUSB := fs.Bool("allow-non-usb", false, "aceptar un disco que no sea USB")
	verify := fs.Bool("verify", false, "releer el disco y comparar con la imagen al terminar")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Err, "argumentos de mas: %v\n", fs.Args())
		return exitUsage
	}
	if *imagePath == "" {
		fmt.Fprintln(a.Err, "error: falta --image")
		return exitUsage
	}
	if *target < 0 {
		fmt.Fprintln(a.Err, "error: falta --target-disk")
		return exitUsage
	}

	st, err := os.Stat(*imagePath)
	if err != nil {
		fmt.Fprintf(a.Err, "error: leer la imagen: %v\n", err)
		return exitError
	}
	if st.IsDir() || st.Size() == 0 {
		fmt.Fprintf(a.Err, "error: %q no es una imagen de disco\n", *imagePath)
		return exitError
	}

	if a.Enumerate == nil {
		fmt.Fprintln(a.Err, "error: falta el enumerador de discos")
		return exitError
	}
	inv, err := a.Enumerate()
	if err != nil {
		fmt.Fprintf(a.Err, "error: leer el inventario de discos: %v\n", err)
		return exitError
	}

	d, ok := findDisk(inv.Disks, uint32(*target))
	if !ok {
		fmt.Fprintf(a.Err, "error: no existe el disco %d; estan el %s\n", *target, diskNumbers(inv.Disks))
		return exitError
	}

	if err := safety.Authorize(d, safety.Request{
		DiskNumber:           d.Number,
		ExpectSerial:         *expectSerial,
		AllowNonUSB:          *allowNonUSB,
		ProtectedMountPoints: a.ProtectedMounts,
	}); err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			fmt.Fprintf(a.Err, "error: no se puede escribir en el disco %d\n", d.Number)
			for _, r := range refusal.Reasons {
				fmt.Fprintf(a.Err, "  - %s\n", r.Detail)
			}
			fmt.Fprintln(a.Err, "\nNo se ha abierto el disco ni se ha escrito nada.")
			return exitError
		}
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	// La imagen tiene que caber. Se comprueba antes de abrir nada.
	if st.Size() > d.SizeBytes {
		fmt.Fprintf(a.Err, "error: la imagen ocupa %s y el disco %s: no cabe\n",
			blockdev.HumanSize(st.Size()), blockdev.HumanSize(d.SizeBytes))
		return exitError
	}

	fmt.Fprintf(a.Out, "imagen     : %s (%s)\n", *imagePath, blockdev.HumanSize(st.Size()))
	fmt.Fprintf(a.Out, "disco      : %d  %s [%s]\n", d.Number, d.Model, d.Bus)
	fmt.Fprintf(a.Out, "serial     : %s\n", d.Serial)
	fmt.Fprintf(a.Out, "se destruye : todo el contenido del disco %d\n", d.Number)

	if !windisk.IsElevated() {
		fmt.Fprintln(a.Err, "error: para escribir en un disco fisico hace falta una consola elevada")
		fmt.Fprintln(a.Err, "       abre PowerShell como administrador y repite el comando")
		fmt.Fprintln(a.Err, "       no se ha abierto el disco ni se ha escrito nada")
		return exitError
	}

	disk, err := windisk.OpenExclusive(int(d.Number))
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}
	defer func() { _ = disk.Close() }()

	for _, path := range disk.LockedVolumes() {
		a.phase("volumen bloqueado y desmontado: %s", path)
	}
	a.phase("disco %d abierto en exclusiva, %s", disk.Number(), blockdev.HumanSize(disk.Size()))

	escritos, err := a.copyToDisk(*imagePath, disk, st.Size())
	if err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	// La imagen se construyo para un tamano concreto: si el disco es mayor, la copia
	// de seguridad del GPT quedo donde acababa la imagen, no donde acaba el disco.
	if err := a.retargetGPT(disk); err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	// Sin este volcado, el "listo" seria mentira: los datos estarian en la cache de
	// Windows y el disco podria desconectarse con medio sistema dentro.
	if err := disk.Sync(); err != nil {
		fmt.Fprintf(a.Err, "error: %v\n", err)
		return exitError
	}

	if *verify {
		if err := a.verifyDisk(*imagePath, disk, escritos); err != nil {
			fmt.Fprintf(a.Err, "error: verificar: %v\n", err)
			return exitError
		}
		a.phase("verificado: el disco coincide con la imagen")
	}

	a.phase("escritura terminada; ya se puede desconectar el disco")
	return exitOK
}

// copyToDisk vuelca la imagen en el disco en bloques grandes y en orden.
func (a *App) copyToDisk(imagePath string, disk *windisk.Disk, size int64) (int64, error) {
	src, err := os.Open(imagePath)
	if err != nil {
		return 0, fmt.Errorf("abrir la imagen: %w", err)
	}
	defer src.Close()

	buf := make([]byte, flashChunk)
	started := time.Now()
	var escritos int64

	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := disk.WriteAt(buf[:n], escritos); werr != nil {
				return escritos, fmt.Errorf("escribir en el disco en %d: %w", escritos, werr)
			}
			escritos += int64(n)

			elapsed := time.Since(started)
			rate := float64(escritos) / elapsed.Seconds()
			eta := "-"
			if rate > 0 && size > escritos {
				eta = time.Duration(float64(size-escritos) / rate * float64(time.Second)).Round(time.Second).String()
			}
			fmt.Fprintf(a.Err, "\r[%s] %s / %s · %.0f MB/s · quedan %s        ",
				time.Now().Format("15:04:05"),
				blockdev.HumanSize(escritos), blockdev.HumanSize(size),
				rate/(1<<20), eta)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return escritos, fmt.Errorf("leer la imagen: %w", rerr)
		}
	}
	fmt.Fprintln(a.Err)

	elapsed := time.Since(started).Round(time.Second)
	if elapsed <= 0 {
		elapsed = time.Second
	}
	a.phase("escritos %s en %s (%.0f MB/s)",
		blockdev.HumanSize(escritos), elapsed, float64(escritos)/elapsed.Seconds()/(1<<20))
	return escritos, nil
}

// verifyDisk relee el disco y lo compara con la imagen, byte a byte.
func (a *App) verifyDisk(imagePath string, disk *windisk.Disk, size int64) error {
	src, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("abrir la imagen: %w", err)
	}
	defer src.Close()

	esperado := make([]byte, flashChunk)
	leido := make([]byte, flashChunk)
	started := time.Now()
	var comparados int64

	for {
		n, rerr := src.Read(esperado)
		if n > 0 {
			if _, werr := disk.ReadAt(leido[:n], comparados); werr != nil {
				return fmt.Errorf("leer el disco en %d: %w", comparados, werr)
			}
			if !bytes.Equal(esperado[:n], leido[:n]) {
				return fmt.Errorf("el disco y la imagen difieren en el byte %d", comparados+int64(primeraDiferencia(esperado[:n], leido[:n])))
			}
			comparados += int64(n)

			elapsed := time.Since(started)
			rate := float64(comparados) / elapsed.Seconds()
			fmt.Fprintf(a.Err, "\r[%s] verificado %s / %s · %.0f MB/s        ",
				time.Now().Format("15:04:05"),
				blockdev.HumanSize(comparados), blockdev.HumanSize(size), rate/(1<<20))
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("leer la imagen: %w", rerr)
		}
	}
	fmt.Fprintln(a.Err)
	return nil
}

// primeraDiferencia devuelve el indice del primer byte distinto.
func primeraDiferencia(a, b []byte) int {
	for i := range a {
		if a[i] != b[i] {
			return i
		}
	}
	return 0
}

// retargetGPT mueve la cabecera de respaldo del GPT al final del disco real.
//
// Una imagen de 8 GiB copiada a un disco de 465 GiB deja el respaldo a 8 GiB, en
// medio de la nada. Linux arranca igual porque usa el primario, pero sfdisk
// --verify y cualquier editor de particiones lo denuncian, y con razon: el respaldo
// no esta donde debe.
//
// Las particiones no se tocan: siguen donde estaban y sobran 457 GiB sin usar.
func (a *App) retargetGPT(disk *windisk.Disk) error {
	table, actual, err := gpt.Read(disk)
	if err != nil {
		return fmt.Errorf("leer la tabla copiada: %w", err)
	}

	nuevo, err := gpt.NewLayout(disk.Size(), disk.SectorSize())
	if err != nil {
		return fmt.Errorf("recalcular el layout del disco: %w", err)
	}

	if actual.TotalSectors == nuevo.TotalSectors {
		return nil
	}

	if err := gpt.Commit(disk, table, nuevo); err != nil {
		return fmt.Errorf("reubicar el GPT al tamano del disco: %w", err)
	}

	sinUsar := disk.Size() - int64(table.Partitions[len(table.Partitions)-1].LastLBA+1)*int64(nuevo.SectorSize)
	if sinUsar > 0 {
		a.phase("el disco queda con %s sin particionar", blockdev.HumanSize(sinUsar))
	}
	return nil
}
