package cli

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"sort"
	"strings"
	"time"

	"distrogo/pkg/accounts"
	"distrogo/pkg/bootcfg"
	"distrogo/pkg/efilayout"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
	"distrogo/pkg/rootfs"
)

// installTarget deja el disco arrancando el sistema instalado, no el live.
//
// Copia al ESP el kernel y el initrd que ya estan dentro del rootfs, escribe un
// grub.cfg que apunta a la particion root por UUID y anade el /etc/fstab del
// sistema instalado.
func (a *App) installTarget(esp *fat32.FileSystem, root *ext4fs.FS, espInfo fat32.Info, rootInfo ext4fs.Info, title string) (efilayout.Result, error) {
	var res efilayout.Result

	kernel, initrd, err := pickKernel(root)
	if err != nil {
		return res, err
	}

	for _, path := range []string{kernel, initrd} {
		rc, size, err := root.OpenFile(path)
		if err != nil {
			return res, err
		}
		werr := esp.ReplaceFile(path, rc)
		_ = rc.Close()
		if werr != nil {
			return res, fmt.Errorf("copiar %q al ESP: %w", path, werr)
		}
		res.Files++
		res.Bytes += size
	}

	// El kernel arranca el sistema del disco: root por UUID, en lectura-escritura.
	// Ojo con el prefijo: sin "root=" el parametro UUID= es invisible para el
	// kernel y el initramfs se queda sin raiz.
	args := []string{"root=" + bootcfg.UUIDSpec(rootInfo.UUID), "rw", "quiet", "splash"}
	cfg := efilayout.GrubCfg(title, args, kernel, initrd)
	if err := esp.ReplaceFile("boot/grub/grub.cfg", strings.NewReader(cfg)); err != nil {
		return res, fmt.Errorf("escribir el grub.cfg: %w", err)
	}
	res.Files++
	res.Bytes += int64(len(cfg))

	fstab, err := bootcfg.Fstab([]bootcfg.Mount{
		{Spec: bootcfg.UUIDSpec(rootInfo.UUID), Dir: "/", Type: "ext4", Options: "errors=remount-ro", Pass: 1},
		{Spec: bootcfg.UUIDSpec(espInfo.UUID()), Dir: "/boot/efi", Type: "vfat", Options: "umask=0077", Pass: 2},
	})
	if err != nil {
		return res, err
	}
	if err := root.OverwriteFile("etc/fstab", []byte(fstab)); err != nil {
		return res, fmt.Errorf("escribir el fstab: %w", err)
	}

	return res, nil
}

// pickKernel busca en /boot el par kernel e initrd mas moderno.
func pickKernel(root *ext4fs.FS) (kernel, initrd string, err error) {
	entries, err := root.List("boot")
	if err != nil {
		return "", "", fmt.Errorf("listar /boot: %w", err)
	}

	var versions []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if v, ok := strings.CutPrefix(e.Name, "vmlinuz-"); ok {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return "", "", errors.New("no hay ningun vmlinuz en /boot: el rootfs no parece un sistema instalable")
	}

	// De mayor a menor: nos quedamos con el kernel mas nuevo que tenga initrd.
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	for _, v := range versions {
		k := "boot/vmlinuz-" + v
		i := "boot/initrd.img-" + v
		if _, err := root.Stat(i); err != nil {
			continue
		}
		return k, i, nil
	}
	return "", "", fmt.Errorf("hay kernel (%s) pero ninguno tiene initrd", strings.Join(versions, ", "))
}

// maxAccountFile acota la lectura de los ficheros de cuentas.
//
// Van en kilobytes, no en megabytes, asi que 1 MiB es margen de sobra y a la vez
// impide que un /etc/passwd corrupto se lleve por delante la memoria del proceso.
const maxAccountFile = 1 << 20

// userResult resume lo que se dejo escrito para la cuenta.
type userResult struct {
	User       string
	UID        int
	Home       string
	AdminGroup string
	HomeFiles  int64
	HomeDirs   int64
}

// createUser crea la cuenta del sistema instalado.
//
// El ISO no trae usuario de persona: solo root bloqueado, porque es casper quien
// crea el live al arrancar (DG-30). Esto lo deja listo para el primer arranque:
// entra en /etc/passwd y /etc/shadow, esta en el grupo que da sudo y tiene home
// con el contenido de /etc/skel.
//
// El orden importa. Primero el home y su contenido, y solo al final las lineas de
// las cuentas: si algo falla a medias, el sistema no se queda con un usuario que
// existe en /etc/passwd pero no puede entrar.
func (a *App) createUser(root *ext4fs.FS, u accounts.User) (userResult, error) {
	var res userResult
	res.User = u.Name
	res.UID = u.UID
	res.Home = u.Home

	passwd, err := readAccountFile(root, "etc/passwd")
	if err != nil {
		return res, err
	}
	group, err := readAccountFile(root, "etc/group")
	if err != nil {
		return res, err
	}
	shadow, err := readAccountFile(root, "etc/shadow")
	if err != nil {
		return res, err
	}
	sudoers, err := readAccountFile(root, "etc/sudoers")
	if err != nil {
		return res, err
	}

	if hasAccount(passwd, u.Name) {
		return res, fmt.Errorf("la cuenta %q ya existe en /etc/passwd", u.Name)
	}

	// Cuales dan privilegios lo dice la propia distro, no nosotros.
	admin, err := accounts.AdminGroup(sudoers, group)
	if err != nil {
		return res, fmt.Errorf("resolver el grupo de administracion: %w", err)
	}
	res.AdminGroup = admin
	u.Groups = []string{admin}

	// 1. El home, del usuario y no de root como /etc/skel.
	homePath := pathOf(u.Home)
	if err := ensureDir(root, pathOf(accounts.DefaultHomeBase)); err != nil {
		return res, err
	}
	if err := root.Mkdir(homePath); err != nil {
		return res, fmt.Errorf("crear el home %q: %w", u.Home, err)
	}

	tgt := root.Target()
	home := rootfs.Entry{
		Path: homePath,
		Type: rootfs.TypeDir,
		Mode: iofs.ModeDir | 0o755,
		UID:  uint32(u.UID),
		GID:  uint32(u.GID),
	}
	if err := tgt.Chmod(home); err != nil {
		return res, fmt.Errorf("ajustar el modo del home: %w", err)
	}
	if err := tgt.Chown(home); err != nil {
		return res, fmt.Errorf("ajustar el dueno del home: %w", err)
	}

	// 2. El contenido de /etc/skel, con el mismo Populate del resto del sistema.
	stats, err := rootfs.Populate(
		rootfs.Rebase(ext4fs.Source(root, "etc/skel"), homePath),
		tgt,
		rootfs.PopulateOptions{TolerateUnsupported: true},
	)
	if err != nil {
		return res, fmt.Errorf("copiar /etc/skel al home: %w", err)
	}
	res.HomeFiles = stats.Files
	res.HomeDirs = stats.Dirs

	// 3. Las lineas, al final. La contrasena va ya hasheada: en claro no llega
	// hasta aqui.
	passwdLine, err := u.Passwd()
	if err != nil {
		return res, err
	}
	shadowLine, err := u.Shadow(time.Now())
	if err != nil {
		return res, err
	}
	groupContent, err := accounts.AddMembers(group, admin, u.Name)
	if err != nil {
		return res, err
	}

	if err := root.AppendFile("etc/passwd", []byte(lineToAppend(passwd, passwdLine))); err != nil {
		return res, fmt.Errorf("escribir /etc/passwd: %w", err)
	}
	if err := root.AppendFile("etc/shadow", []byte(lineToAppend(shadow, shadowLine))); err != nil {
		return res, fmt.Errorf("escribir /etc/shadow: %w", err)
	}
	// /etc/group no se puede ampliar: hay que meter al usuario en una linea que
	// ya existe, asi que se reescribe sobre el mismo inodo.
	if err := root.OverwriteFile("etc/group", []byte(groupContent)); err != nil {
		return res, fmt.Errorf("escribir /etc/group: %w", err)
	}

	return res, nil
}

// readAccountFile lee un fichero de cuentas del volumen, con tope de tamano.
func readAccountFile(root *ext4fs.FS, path string) (string, error) {
	rc, size, err := root.OpenFile(path)
	if err != nil {
		return "", fmt.Errorf("leer %q: %w", path, err)
	}
	defer rc.Close()

	if size > maxAccountFile {
		return "", fmt.Errorf("%q ocupa %d bytes: no parece un fichero de cuentas", path, size)
	}

	data, err := io.ReadAll(io.LimitReader(rc, maxAccountFile+1))
	if err != nil {
		return "", fmt.Errorf("leer %q: %w", path, err)
	}
	return string(data), nil
}

// lineToAppend devuelve lo que hay que anadir para dejar la linea al final.
//
// Los ficheros de cuentas acabados en el ultimo caracter, sin salto, son
// corrientes; anadir a pelo juntaria dos cuentas en la misma linea.
func lineToAppend(content, line string) string {
	if content != "" && !strings.HasSuffix(content, "\n") {
		return "\n" + line + "\n"
	}
	return line + "\n"
}

// hasAccount dice si el nombre ya aparece como cuenta en /etc/passwd.
func hasAccount(passwd, name string) bool {
	for _, line := range strings.Split(passwd, "\n") {
		if usuario, _, ok := strings.Cut(line, ":"); ok && usuario == name {
			return true
		}
	}
	return false
}

// ensureDir crea un directorio si no existe todavia.
func ensureDir(root *ext4fs.FS, path string) error {
	entry, err := root.Stat(path)
	if err == nil {
		if !entry.IsDir {
			return fmt.Errorf("%q existe y no es un directorio", path)
		}
		return nil
	}
	if err := root.Mkdir(path); err != nil {
		return fmt.Errorf("crear %q: %w", path, err)
	}
	return nil
}

// pathOf pasa una ruta absoluta a la forma del contrato: sin barra inicial.
func pathOf(p string) string {
	return strings.TrimPrefix(strings.TrimSpace(p), "/")
}

// buildAccount arma la cuenta de usuario a partir de lo que pida la CLI.
//
// Con --password-hash no se pregunta nada: sirve para scripts y para pruebas
// desatendidas. Sin el, la contrasena se pide dos veces por el terminal, sin eco
// y con mascara.
func (a *App) buildAccount(name string, uid int, hash string) (accounts.User, error) {
	u, err := accounts.New(name, uid)
	if err != nil {
		return accounts.User{}, err
	}

	if hash != "" {
		if !strings.HasPrefix(hash, "$6$") {
			return accounts.User{}, fmt.Errorf("--password-hash tiene que ser un hash SHA-512 ($6$...), no %q", hash)
		}
		u.Password = hash
		return u, nil
	}

	clave, err := readNewPassword(name, promptPassword, a.Err)
	if err != nil {
		if errors.Is(err, ErrNoConsole) {
			return accounts.User{}, fmt.Errorf("%w: usa --password-hash para instalaciones sin terminal", err)
		}
		return accounts.User{}, err
	}

	hashed, err := accounts.Hash(clave)
	if err != nil {
		return accounts.User{}, err
	}
	u.Password = hashed
	return u, nil
}
