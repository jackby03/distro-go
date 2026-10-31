package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

func cuenta(base User) User {
	base.Name = "demo"
	base.UID = 1000
	base.GID = 1000
	base.Home = "/home/demo"
	base.Shell = "/bin/bash"
	base.Gecos = "Demo Daniels"
	base.Password = "$6$salt$hash"
	return base
}

func TestNewDerivesHomeFromUserName(t *testing.T) {
	for _, nombre := range []string{"demo", "ana", "maria", "dev", "usuario_largo-1"} {
		u, err := New(nombre, 1000)
		if err != nil {
			t.Fatalf("%s: error inesperado: %v", nombre, err)
		}

		quiere := "/home/" + nombre
		if u.Home != quiere {
			t.Errorf("%s: home = %q, queria %q", nombre, u.Home, quiere)
		}

		// El home tiene que aparecer tambien en la linea de /etc/passwd.
		linea, err := u.Passwd()
		if err != nil {
			t.Fatalf("%s: error inesperado: %v", nombre, err)
		}
		if !strings.Contains(linea, ":"+quiere+":") {
			t.Errorf("%s: la linea no lleva el home derivado: %q", nombre, linea)
		}
	}
}

func TestNewTwoUsersDoNotShareHome(t *testing.T) {
	ana, err := New("ana", 1000)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	luis, err := New("luis", 1001)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if ana.Home == luis.Home {
		t.Fatalf("dos usuarios no pueden compartir home: %q", ana.Home)
	}
	if ana.Home != "/home/ana" || luis.Home != "/home/luis" {
		t.Fatalf("homes = %q y %q", ana.Home, luis.Home)
	}
}

func TestNewHasPrivateGroupAndNoExtraGroups(t *testing.T) {
	u, err := New("demo", 1000)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if u.UID != u.GID {
		t.Fatalf("el grupo propio tiene que tener el numero del UID: uid %d, gid %d", u.UID, u.GID)
	}
	// Los grupos adicionales dependen de la distro y los pone AdminGroup.
	if len(u.Groups) != 0 {
		t.Fatalf("New no debe inventar grupos: %v", u.Groups)
	}
}

func TestNewRejectsInvalidName(t *testing.T) {
	for _, nombre := range []string{"", "Demo", "con espacio", "con:dos", "1numero"} {
		if _, err := New(nombre, 1000); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("%q: err = %v, queria ErrInvalidUser", nombre, err)
		}
	}
}

func TestHashProducesSHA512Crypt(t *testing.T) {
	hash, err := Hash("correcta")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !strings.HasPrefix(hash, "$6$") {
		t.Fatalf("hash = %q, queria prefijo $6$", hash)
	}
	if strings.Contains(hash, "correcta") {
		t.Fatalf("el hash no puede contener la contrasena: %q", hash)
	}

	// Y el mismo hash tiene que validar la contrasena con el verificador de la
	// libreria, que es lo que hara pam al iniciar sesion.
	if err := sha512_crypt.New().Verify(hash, []byte("correcta")); err != nil {
		t.Fatalf("el hash no valida su propia contrasena: %v", err)
	}
	if err := sha512_crypt.New().Verify(hash, []byte("otra")); err == nil {
		t.Fatal("una contrasena distinta no deberia validar")
	}
}

func TestHashRejectsEmptyPassword(t *testing.T) {
	if _, err := Hash(""); !errors.Is(err, ErrInvalidUser) {
		t.Fatalf("err = %v, queria ErrInvalidUser", err)
	}
}

func TestHashIsRandomPerCall(t *testing.T) {
	a, err := Hash("correcta")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	b, err := Hash("correcta")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if a == b {
		t.Fatal("dos hashes de la misma contrasena no pueden coincidir: la sal no es aleatoria")
	}
}

func TestUserPasswdLine(t *testing.T) {
	line, err := cuenta(User{}).Passwd()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	want := "demo:x:1000:1000:Demo Daniels:/home/demo:/bin/bash"
	if line != want {
		t.Fatalf("linea = %q, queria %q", line, want)
	}
}

func TestUserPasswdUsesDefaultShell(t *testing.T) {
	u := cuenta(User{})
	u.Shell = ""

	line, err := u.Passwd()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !strings.HasSuffix(line, ":"+DefaultShell) {
		t.Fatalf("linea = %q, queria terminar en %q", line, DefaultShell)
	}
}

func TestUserShadowLine(t *testing.T) {
	// 20.000 dias desde 1970 son el 4 de octubre de 2024.
	now := time.Unix(int64(20000)*86400, 0)

	line, err := cuenta(User{}).Shadow(now)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	want := "demo:$6$salt$hash:20000:0:99999:7:::"
	if line != want {
		t.Fatalf("linea = %q, queria %q", line, want)
	}
}

func TestValidateRejectsBrokenFields(t *testing.T) {
	casos := map[string]func(*User){
		"nombre vacio":                  func(u *User) { u.Name = "" },
		"nombre con mayusculas":         func(u *User) { u.Name = "Demo" },
		"nombre con dos puntos":         func(u *User) { u.Name = "ja:ck" },
		"nombre con espacio":            func(u *User) { u.Name = "ja ck" },
		"nombre que empieza por digito": func(u *User) { u.Name = "1demo" },
		"uid de root":                   func(u *User) { u.UID = 0 },
		"gid negativo":                  func(u *User) { u.GID = -1 },
		"home relativo":                 func(u *User) { u.Home = "home/demo" },
		"home con dos puntos":           func(u *User) { u.Home = "/home/ja:ck" },
		"shell relativa":                func(u *User) { u.Shell = "bash" },
		"gecos con dos puntos":          func(u *User) { u.Gecos = "Demo:Daniels" },
		"grupo invalido":                func(u *User) { u.Groups = []string{"Sudo"} },
		"grupo igual al usuario":        func(u *User) { u.Groups = []string{"demo"} },
	}

	for nombre, romper := range casos {
		u := cuenta(User{})
		romper(&u)
		if err := u.Validate(); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("%s: err = %v, queria ErrInvalidUser", nombre, err)
		}
	}
}

func TestValidateAcceptsNormalUser(t *testing.T) {
	u := cuenta(User{})
	u.Groups = []string{"sudo", "adm", "cdrom"}

	if err := u.Validate(); err != nil {
		t.Fatalf("una cuenta normal no deberia fallar: %v", err)
	}
}

func TestAddMembersAppendsToExistingGroup(t *testing.T) {
	group := "root:x:0:\nsudo:x:27:\nadm:x:4:syslog\nplugdev:x:46:\n"

	got, err := AddMembers(group, "sudo", "demo")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !strings.Contains(got, "sudo:x:27:demo") {
		t.Fatalf("no se anadio el miembro: %q", got)
	}
	// El resto del fichero no se toca.
	for _, linea := range []string{"root:x:0:", "adm:x:4:syslog", "plugdev:x:46:"} {
		if !strings.Contains(got, linea) {
			t.Fatalf("se perdio la linea %q: %q", linea, got)
		}
	}
}

func TestAddMembersKeepsExistingMembers(t *testing.T) {
	group := "adm:x:4:syslog,demo\n"

	got, err := AddMembers(group, "adm", "demo", "ana")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "adm:x:4:syslog,demo,ana\n" {
		t.Fatalf("contenido = %q, queria %q", got, "adm:x:4:syslog,demo,ana\n")
	}
}

func TestAddMembersIsIdempotent(t *testing.T) {
	group := "sudo:x:27:\n"

	una, err := AddMembers(group, "sudo", "demo")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	dos, err := AddMembers(una, "sudo", "demo")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if una != dos {
		t.Fatalf("llamar dos veces cambio el fichero: %q y %q", una, dos)
	}
	if strings.Count(dos, "demo") != 1 {
		t.Fatalf("el miembro esta duplicado: %q", dos)
	}
}

func TestAddMembersRejectsUnknownGroup(t *testing.T) {
	if _, err := AddMembers("sudo:x:27:\n", "wheel", "demo"); err == nil {
		t.Fatal("un grupo que no existe no puede aceptar miembros")
	}
}

func TestAddMembersIgnoresCommentsAndBlankLines(t *testing.T) {
	group := "# sudo:x:27:\n\nsudo:x:27:\n"

	got, err := AddMembers(group, "sudo", "demo")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !strings.HasPrefix(got, "# sudo:x:27:\n") {
		t.Fatalf("se toco el comentario: %q", got)
	}
	if !strings.Contains(got, "sudo:x:27:demo") {
		t.Fatalf("no se anadio el miembro: %q", got)
	}
}

func TestValidateAcceptsUserWithoutPasswordYet(t *testing.T) {
	// Crear el home o la linea de /etc/passwd puede ir antes de tener el hash.
	u := cuenta(User{})
	u.Password = ""

	if err := u.Validate(); err != nil {
		t.Fatalf("sin contrasena todavia deberia validar: %v", err)
	}
	if _, err := u.Passwd(); err != nil {
		t.Fatalf("la linea de passwd no necesita contrasena: %v", err)
	}
}

func TestUserShadowRejectsMissingOrPlaintextPassword(t *testing.T) {
	now := time.Unix(0, 0)

	for nombre, clave := range map[string]string{
		"sin hash":     "",
		"en claro":     "correcta",
		"hash de otro": "$1$salt$hash",
	} {
		u := cuenta(User{})
		u.Password = clave

		if _, err := u.Shadow(now); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("%s: err = %v, queria ErrInvalidUser", nombre, err)
		}
	}
}

func TestAdminGroupReadsDebianSudoers(t *testing.T) {
	sudoers := `#
# This file MUST be edited with the 'visudo' command as root.
#
Defaults	env_reset
root	ALL=(ALL:ALL) ALL
%sudo	ALL=(ALL:ALL) ALL
#includedir /etc/sudoers.d
`
	group := "root:x:0:\nsudo:x:27:\nwheel:x:10:\n"

	got, err := AdminGroup(sudoers, group)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "sudo" {
		t.Fatalf("grupo = %q, queria sudo", got)
	}
}

func TestAdminGroupReadsFedoraSudoers(t *testing.T) {
	// Fedora y Arch conceden a wheel, y no tienen grupo sudo.
	sudoers := "%wheel	ALL=(ALL)	ALL\n"
	group := "root:x:0:\nwheel:x:10:\n"

	got, err := AdminGroup(sudoers, group)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "wheel" {
		t.Fatalf("grupo = %q, queria wheel", got)
	}
}

func TestAdminGroupPrefersSudoWhenBothExist(t *testing.T) {
	sudoers := "%wheel	ALL=(ALL)	ALL\n%sudo	ALL=(ALL:ALL) ALL\n"
	group := "wheel:x:10:\nsudo:x:27:\n"

	got, err := AdminGroup(sudoers, group)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "sudo" {
		t.Fatalf("grupo = %q, queria sudo", got)
	}
}

func TestAdminGroupIgnoresCommentedGrants(t *testing.T) {
	// Una concesion comentada no da privilegios a nadie.
	sudoers := "# %wheel ALL=(ALL) ALL\nroot ALL=(ALL) ALL\n"
	group := "root:x:0:\nsudo:x:27:\nwheel:x:10:\n"

	got, err := AdminGroup(sudoers, group)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "sudo" {
		t.Fatalf("grupo = %q: no se puede elegir un grupo que sudoers no concede", got)
	}
}

func TestAdminGroupFallsBackWhenSudoersHasNoGroupGrant(t *testing.T) {
	// Cuando la concesion vive en /etc/sudoers.d, el fichero principal no la tiene.
	sudoers := "root ALL=(ALL:ALL) ALL\n#includedir /etc/sudoers.d\n"
	group := "root:x:0:\nsudo:x:27:\n"

	got, err := AdminGroup(sudoers, group)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "sudo" {
		t.Fatalf("grupo = %q, queria sudo", got)
	}
}

func TestAdminGroupRequiresTheGroupToExist(t *testing.T) {
	// sudoers concede a sudo, pero el grupo no esta: no se puede usar.
	sudoers := "%sudo ALL=(ALL:ALL) ALL\n"
	group := "root:x:0:\nwheel:x:10:\n"

	got, err := AdminGroup(sudoers, group)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got != "wheel" {
		t.Fatalf("grupo = %q, queria wheel", got)
	}
}

func TestAdminGroupFailsWhenNothingQualifies(t *testing.T) {
	if _, err := AdminGroup("root ALL=(ALL) ALL\n", "root:x:0:\nusers:x:100:\n"); err == nil {
		t.Fatal("sin grupo de administracion se tiene que avisar, no seguir")
	}
}

func TestUserGroupLine(t *testing.T) {
	line, err := cuenta(User{}).Group()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if line != "demo:x:1000:" {
		t.Fatalf("linea = %q, queria demo:x:1000:", line)
	}
}

func TestGroupByGIDAndName(t *testing.T) {
	group := "root:x:0:\nsudo:x:27:\ndemo:x:1000:\n"

	if nombre, ok := GroupByGID(group, 1000); !ok || nombre != "demo" {
		t.Fatalf("GroupByGID(1000) = %q %v", nombre, ok)
	}
	if _, ok := GroupByGID(group, 4242); ok {
		t.Fatal("un GID que no esta no puede devolver grupo")
	}
	if !GroupByName(group, "demo") {
		t.Fatal("GroupByName(demo) deberia encontrarlo")
	}
	if GroupByName(group, "ana") {
		t.Fatal("GroupByName(ana) no deberia encontrarlo")
	}
	// Los comentarios no son grupos.
	if GroupByName("# demo:x:1000:\n", "demo") {
		t.Fatal("una linea comentada no es un grupo")
	}
}
