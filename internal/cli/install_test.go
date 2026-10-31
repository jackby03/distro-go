package cli

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"distrogo/pkg/accounts"
	"distrogo/pkg/blockdev"
	"distrogo/pkg/ext4fs"
	"distrogo/pkg/fat32"
	"distrogo/pkg/rootfs"
)

// TestInstallTargetWritesRootParam es el test que habria cazado el fallo real del
// primer arranque: el parametro del kernel se escribia como UUID=... sin root=,
// y el initramfs se quedaba sin raiz.
func TestInstallTargetWritesRootParam(t *testing.T) {
	const (
		imageSize  = 64 << 20
		espOffset  = 1 << 20
		espSize    = 40 << 20
		rootOffset = espOffset + espSize
		rootSize   = 20 << 20
	)

	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "img"), imageSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	defer img.Close()

	espSection, err := blockdev.NewSection(img, espOffset, espSize)
	if err != nil {
		t.Fatalf("NewSection: %v", err)
	}
	esp, err := fat32.Format(espSection, fat32.Options{Label: "SYSTEM"})
	if err != nil {
		t.Fatalf("fat32.Format: %v", err)
	}

	root, err := ext4fs.Format(img, rootOffset, rootSize, ext4fs.Options{
		Label:      "distrogo-root",
		InodeCount: 2000,
	})
	if err != nil {
		t.Fatalf("ext4fs.Format: %v", err)
	}

	// Un kernel y un initrd falsos, con el nombre que espera la instalacion.
	if err := root.WriteFile("boot/vmlinuz-9.9.9-test", strings.NewReader("kernel")); err != nil {
		t.Fatalf("WriteFile kernel: %v", err)
	}
	if err := root.WriteFile("boot/initrd.img-9.9.9-test", strings.NewReader("initrd")); err != nil {
		t.Fatalf("WriteFile initrd: %v", err)
	}

	rootInfo, err := root.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if _, err := app.installTarget(esp, root, esp.Info(), rootInfo, "Prueba"); err != nil {
		t.Fatalf("installTarget: %v", err)
	}

	// El grub.cfg tiene que arrancar el kernel instalado con root=UUID.
	cfg := readText(t, esp.OpenFile, "boot/grub/grub.cfg")
	for _, want := range []string{
		"root=UUID=" + rootInfo.UUID,
		"/boot/vmlinuz-9.9.9-test",
		"/boot/initrd.img-9.9.9-test",
		"rw",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("el grub.cfg no contiene %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "boot=casper") {
		t.Error("una instalacion real no deberia arrancar en modo live")
	}

	// Y el fstab tiene el root y el ESP, cada uno por su UUID.
	fstab := readText(t, root.OpenFile, "etc/fstab")
	for _, want := range []string{
		"UUID=" + rootInfo.UUID,
		" / ",
		"ext4",
		"UUID=" + esp.Info().UUID(),
		"/boot/efi",
		"vfat",
	} {
		if !strings.Contains(fstab, want) {
			t.Errorf("el fstab no contiene %q:\n%s", want, fstab)
		}
	}

	// Y los dos ficheros del kernel tienen que estar en el ESP.
	for _, path := range []string{"boot/vmlinuz-9.9.9-test", "boot/initrd.img-9.9.9-test"} {
		if _, err := esp.Stat(path); err != nil {
			t.Errorf("%q no esta en el ESP: %v", path, err)
		}
	}
}

// readText lee un fichero del volumen con el abridor que le pasen.
func readText(t *testing.T, open func(string) (io.ReadCloser, int64, error), path string) string {
	t.Helper()
	rc, _, err := open(path)
	if err != nil {
		t.Fatalf("abrir %q: %v", path, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("leer %q: %v", path, err)
	}
	return string(body)
}

// DebianSudoers es el /etc/sudoers que traen Debian, Ubuntu y Mint.
const debianSudoers = `#
# This file MUST be edited with the 'visudo' command as root.
#
Defaults	env_reset
root	ALL=(ALL:ALL) ALL
%sudo	ALL=(ALL:ALL) ALL
#includedir /etc/sudoers.d
`

// newUserFS monta un ext4 con lo justo que hay en /etc y en /etc/skel.
func newUserFS(t *testing.T) *ext4fs.FS {
	t.Helper()

	const (
		imageSize  = 64 << 20
		rootOffset = 1 << 20
		rootSize   = 40 << 20
	)

	img, err := blockdev.CreateImage(filepath.Join(t.TempDir(), "user.img"), imageSize)
	if err != nil {
		t.Fatalf("CreateImage: %v", err)
	}
	t.Cleanup(func() { _ = img.Close() })

	root, err := ext4fs.Format(img, rootOffset, rootSize, ext4fs.Options{
		Label:      "distrogo-root",
		InodeCount: 4000,
	})
	if err != nil {
		t.Fatalf("ext4fs.Format: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	ficheros := map[string]string{
		// Sin salto final a proposito: es lo que hay en muchas distros y
		// concatenar a pelo juntaria dos cuentas en la misma linea.
		"etc/passwd":                    "root:x:0:0:root:/root:/bin/bash",
		"etc/shadow":                    "root:*:20461:0:99999:7:::",
		"etc/group":                     "root:x:0:\nsudo:x:27:\nadm:x:4:syslog\n",
		"etc/sudoers":                   debianSudoers,
		"etc/skel/.bashrc":              "# bashrc\n",
		"etc/skel/.profile":             "# profile\n",
		"etc/skel/.config/monitors.xml": "<monitors/>",
	}
	for path, body := range ficheros {
		if err := root.WriteFile(path, strings.NewReader(body)); err != nil {
			t.Fatalf("WriteFile %q: %v", path, err)
		}
	}
	if err := root.Mkdir("home"); err != nil {
		t.Fatalf("Mkdir home: %v", err)
	}

	return root
}

// nuevaCuenta prepara la cuenta como lo hara la CLI, con hash y sin grupos.
func nuevaCuenta(t *testing.T, nombre string, uid int) accounts.User {
	t.Helper()

	u, err := accounts.New(nombre, uid)
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	hash, err := accounts.Hash("correcta")
	if err != nil {
		t.Fatalf("accounts.Hash: %v", err)
	}
	u.Password = hash
	return u
}

func TestCreateUserWritesAccountsAndHome(t *testing.T) {
	root := newUserFS(t)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	res, err := app.createUser(root, nuevaCuenta(t, "ana", 1000))
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}

	if res.AdminGroup != "sudo" {
		t.Errorf("grupo de administracion = %q, queria sudo", res.AdminGroup)
	}
	if res.Home != "/home/ana" {
		t.Errorf("home = %q", res.Home)
	}
	if res.HomeFiles != 3 {
		t.Errorf("ficheros copiados al home = %d, queria 3", res.HomeFiles)
	}

	// /etc/passwd: la cuenta nueva, y root intacto en su linea.
	passwd := readText(t, root.OpenFile, "etc/passwd")
	for _, want := range []string{
		"root:x:0:0:root:/root:/bin/bash",
		"ana:x:1000:1000::/home/ana:/bin/bash",
	} {
		if !strings.Contains(passwd, want) {
			t.Errorf("el passwd no contiene %q:\n%s", want, passwd)
		}
	}
	// El fichero original no acababa en salto: no se pueden pegar dos cuentas.
	if strings.Contains(passwd, "/bin/bashana:x:") {
		t.Fatalf("se pegaron dos cuentas en la misma linea:\n%s", passwd)
	}

	// /etc/shadow: hash SHA-512 y ninguna contrasena en claro.
	shadow := readText(t, root.OpenFile, "etc/shadow")
	if !strings.Contains(shadow, "ana:$6$") {
		t.Errorf("la cuenta no lleva hash $6$:\n%s", shadow)
	}
	if strings.Contains(shadow, "correcta") {
		t.Fatalf("la contrasena en claro acabo en /etc/shadow:\n%s", shadow)
	}

	// /etc/group: el usuario en el grupo que da sudo, sin tocar los demas.
	group := readText(t, root.OpenFile, "etc/group")
	if !strings.Contains(group, "sudo:x:27:ana") {
		t.Errorf("ana no esta en sudo:\n%s", group)
	}
	if !strings.Contains(group, "adm:x:4:syslog") {
		t.Errorf("se perdio el grupo adm:\n%s", group)
	}

	// Y el home con el contenido de skel.
	for _, path := range []string{
		"home/ana/.bashrc",
		"home/ana/.profile",
		"home/ana/.config/monitors.xml",
	} {
		if _, err := root.Stat(path); err != nil {
			t.Errorf("falta %q en el home: %v", path, err)
		}
	}
}

func TestCreateUserHomeBelongsToTheUser(t *testing.T) {
	root := newUserFS(t)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	if _, err := app.createUser(root, nuevaCuenta(t, "ana", 1000)); err != nil {
		t.Fatalf("createUser: %v", err)
	}

	// El home tiene que ser del usuario: si se queda de root, no puede escribir
	// en su propio directorio al iniciar sesion.
	info, err := root.Stat("home/ana")
	if err != nil {
		t.Fatalf("no existe home/ana: %v", err)
	}
	if !info.IsDir {
		t.Fatal("home/ana deberia ser un directorio")
	}

	// Y el dueno se comprueba por el mismo camino que usa el lector del volumen.
	src := ext4fs.Source(root, "home")
	var uid, gid uint32
	encontrado := false
	err = src.Walk(func(e rootfs.Entry) error {
		if e.Path == "ana" {
			uid, gid, encontrado = e.UID, e.GID, true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !encontrado {
		t.Fatal("el recorrido no vio el home")
	}
	if uid != 1000 || gid != 1000 {
		t.Fatalf("el home quedo como %d:%d, queria 1000:1000", uid, gid)
	}
}

func TestCreateUserRefusesExistingAccount(t *testing.T) {
	root := newUserFS(t)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	u := nuevaCuenta(t, "ana", 1000)

	if _, err := app.createUser(root, u); err != nil {
		t.Fatalf("la primera vez deberia funcionar: %v", err)
	}

	antes := readText(t, root.OpenFile, "etc/passwd")
	if _, err := app.createUser(root, u); err == nil {
		t.Fatal("crear dos veces la misma cuenta tiene que fallar")
	}
	if despues := readText(t, root.OpenFile, "etc/passwd"); despues != antes {
		t.Fatalf("el fichero cambio tras el error:\n%s\n---\n%s", antes, despues)
	}
}

func TestCreateUserFailsWithoutAdminGroup(t *testing.T) {
	root := newUserFS(t)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	// Sin grupo que de sudo ni sustituto: mejor abortar que dejar una cuenta sin
	// privilegios y que el usuario lo descubra al usarla.
	if err := root.OverwriteFile("etc/sudoers", []byte("root ALL=(ALL) ALL\n")); err != nil {
		t.Fatalf("OverwriteFile sudoers: %v", err)
	}
	if err := root.OverwriteFile("etc/group", []byte("root:x:0:\nusers:x:100:\n")); err != nil {
		t.Fatalf("OverwriteFile group: %v", err)
	}

	antes := readText(t, root.OpenFile, "etc/passwd")
	if _, err := app.createUser(root, nuevaCuenta(t, "ana", 1000)); err == nil {
		t.Fatal("sin grupo de administracion no se puede crear la cuenta")
	}
	if despues := readText(t, root.OpenFile, "etc/passwd"); despues != antes {
		t.Fatalf("se toco /etc/passwd antes de validar:\n%s", despues)
	}
	// Y no debe quedar un home a medias.
	if _, err := root.Stat("home/ana"); err == nil {
		t.Fatal("el home se creo aunque la cuenta no se pudo terminar")
	}
}

func TestBuildAccountWithPasswordHashDoesNotAsk(t *testing.T) {
	// Con --password-hash no hay terminal de por medio: sirve para scripts.
	hash, err := accounts.Hash("correcta")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	u, err := app.buildAccount("ana", 1000, hash)
	if err != nil {
		t.Fatalf("buildAccount: %v", err)
	}
	if u.Password != hash {
		t.Fatalf("no uso el hash dado")
	}
	if u.Home != "/home/ana" || u.UID != 1000 || u.GID != 1000 {
		t.Fatalf("cuenta mal armada: %+v", u)
	}
	// Y la linea de shadow sale con ese hash.
	if _, err := u.Shadow(time.Now()); err != nil {
		t.Fatalf("Shadow: %v", err)
	}
}

func TestBuildAccountRejectsNonSHA512Hash(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	for _, hash := range []string{"correcta", "$1$sal$hash", "$y$j9T$sal$hash"} {
		if _, err := app.buildAccount("ana", 1000, hash); err == nil {
			t.Errorf("%q: deberia rechazarse en la CLI y no llegar al disco", hash)
		}
	}
}

func TestPartitionRequiresInstallForUser(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	// Sin --install no hay sistema donde crear la cuenta: hay que avisar en vez
	// de ignorar la flag en silencio.
	code := app.Run([]string{"partition", "--image", filepath.Join(t.TempDir(), "x.img"), "--user", "ana"})
	if code == exitOK {
		t.Fatal("--user sin --install no puede terminar bien")
	}
	if !strings.Contains(app.Err.(*bytes.Buffer).String(), "--user necesita --install") {
		t.Fatalf("no explico el motivo: %q", app.Err.(*bytes.Buffer).String())
	}
}
