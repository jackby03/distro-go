// Package accounts genera las entradas de /etc/passwd, /etc/shadow y /etc/group
// de la cuenta que se crea durante la instalacion.
//
// Son funciones puras: reciben los datos y devuelven texto. El instalador decide
// donde escribirlos y como fusionarlos con los ficheros que ya trae la distro.
//
// Hacen falta porque el ISO no trae cuenta de usuario: el squashfs solo incluye
// root, bloqueado, y es casper quien crea el usuario live al arrancar. Copiado el
// arbol tal cual, el sistema instalado se queda sin con que iniciar sesion
// (DG-30).
package accounts

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

// DefaultShell es el interprete que se pone si el usuario no pide otro.
const DefaultShell = "/bin/bash"

// DefaultHomeBase es el prefijo de los directorios personales, como en useradd.
const DefaultHomeBase = "/home"

// ErrInvalidUser indica que los datos de la cuenta no sirven para un /etc/passwd.
var ErrInvalidUser = errors.New("datos de cuenta invalidos")

// nombres de usuario validos: minusculas, empiezan por letra o guion bajo, y
// solo llevan letras, digitos, guion, punto y guion bajo. Es la misma regla que
// aplica useradd.
var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

// User describe la cuenta que se crea en el sistema instalado.
type User struct {
	Name string
	UID  int
	GID  int
	// Gecos es el nombre completo. Es opcional.
	Gecos string
	// Home es el directorio personal, siempre con ruta absoluta. Sale del
	// nombre del usuario: New lo rellena y no hay que escribirlo a mano.
	Home string
	// Shell es el interprete de comandos.
	Shell string
	// Groups son los grupos adicionales a los que pertenece, como sudo o adm.
	// No incluye el grupo propio, que sale de GID.
	Groups []string
	// Password es el hash que devuelve Hash, nunca la contrasena en claro.
	Password string
}

// New crea la cuenta con los valores por defecto de una distribucion.
//
// El directorio personal se deriva del nombre, /home/<usuario>, igual que hace
// useradd -m. No se pide como parametro porque no hay motivo para que el home se
// llame distinto al usuario y es facil equivocarse al escribirlo a mano. El grupo
// propio es uno por usuario, con el mismo numero que el UID (useradd -U).
//
// No trae grupos adicionales: cuales dan privilegios depende de la distribucion y
// se resuelve al instalar con AdminGroup.
func New(name string, uid int) (User, error) {
	if !nameRe.MatchString(name) {
		return User{}, fmt.Errorf("%w: nombre %q no valido", ErrInvalidUser, name)
	}
	if uid <= 0 {
		return User{}, fmt.Errorf("%w: el UID %d es de sistema o de root", ErrInvalidUser, uid)
	}

	return User{
		Name:  name,
		UID:   uid,
		GID:   uid,
		Home:  DefaultHomeBase + "/" + name,
		Shell: DefaultShell,
	}, nil
}

// AdminGroup averigua que grupo da privilegios de administracion en la distro.
//
// No se puede dar por hecho que se llame "sudo": Debian, Ubuntu y Mint lo llaman
// asi, pero Fedora y Arch usan "wheel". Quien manda es /etc/sudoers, asi que se
// leen sus lineas de concesion (las que empiezan por %grupo) y se comprueba que el
// grupo exista de verdad en /etc/group.
//
// Se prerieren sudo y wheel por ese orden, que son los dos nombres que se usan.
// Si sudoers no declara ninguno (porque lo deja en /etc/sudoers.d), se cae a esos
// mismos nombres si el grupo existe.
func AdminGroup(sudoers, group string) (string, error) {
	var candidatos []string
	for _, linea := range strings.Split(sudoers, "\n") {
		linea = strings.TrimSpace(linea)
		if linea == "" || strings.HasPrefix(linea, "#") || !strings.HasPrefix(linea, "%") {
			continue
		}
		campos := strings.Fields(linea)
		if len(campos) == 0 {
			continue
		}
		candidatos = append(candidatos, strings.TrimPrefix(campos[0], "%"))
	}

	existe := func(nombre string) bool {
		if nombre == "" {
			return false
		}
		for _, linea := range strings.Split(group, "\n") {
			if linea == "" || strings.HasPrefix(linea, "#") {
				continue
			}
			if campos := strings.SplitN(linea, ":", 2); len(campos) == 2 && campos[0] == nombre {
				return true
			}
		}
		return false
	}

	// Primero los dos nombres habituales, en ese orden.
	for _, preferido := range []string{"sudo", "wheel"} {
		for _, c := range candidatos {
			if c == preferido && existe(c) {
				return c, nil
			}
		}
		if existe(preferido) {
			return preferido, nil
		}
	}

	// Y si no, cualquier otro grupo que sudoers conceda y exista.
	for _, c := range candidatos {
		if existe(c) {
			return c, nil
		}
	}

	return "", fmt.Errorf("no hay ningun grupo de administracion en /etc/sudoers ni en /etc/group")
}

// Validate comprueba que la cuenta se puede escribir en /etc/passwd y
// /etc/group sin romper los ficheros.
//
// Los dos puntos y los saltos de linea son separadores en /etc/passwd, asi que un
// nombre o un comentario que los lleve partiria la linea y dejaria el fichero
// inservible. Se validan aqui y no al escribir.
//
// La contrasena no se mira aqui: /etc/passwd no la lleva, y hay rutas, como crear
// el home, que van antes de tener el hash. Eso lo exige Shadow.
func (u User) Validate() error {
	if !nameRe.MatchString(u.Name) {
		return fmt.Errorf("%w: nombre %q no valido", ErrInvalidUser, u.Name)
	}
	if u.UID <= 0 {
		return fmt.Errorf("%w: el UID %d es de sistema o de root", ErrInvalidUser, u.UID)
	}
	if u.GID <= 0 {
		return fmt.Errorf("%w: el GID %d es de sistema o de root", ErrInvalidUser, u.GID)
	}
	if !strings.HasPrefix(u.Home, "/") || strings.ContainsAny(u.Home, ":\n") {
		return fmt.Errorf("%w: home %q no valido", ErrInvalidUser, u.Home)
	}
	shell := u.Shell
	if shell == "" {
		shell = DefaultShell
	}
	if !strings.HasPrefix(shell, "/") || strings.ContainsAny(shell, ":\n") {
		return fmt.Errorf("%w: shell %q no valida", ErrInvalidUser, shell)
	}
	if strings.ContainsAny(u.Gecos, ":\n") {
		return fmt.Errorf("%w: el comentario lleva dos puntos o salto de linea", ErrInvalidUser)
	}
	for _, g := range u.Groups {
		if !nameRe.MatchString(g) {
			return fmt.Errorf("%w: grupo %q no valido", ErrInvalidUser, g)
		}
		if g == u.Name {
			return fmt.Errorf("%w: el grupo %q no hace falta en la lista, sale del GID", ErrInvalidUser, g)
		}
	}
	return nil
}

// Passwd devuelve la linea de /etc/passwd.
func (u User) Passwd() (string, error) {
	if err := u.Validate(); err != nil {
		return "", err
	}
	shell := u.Shell
	if shell == "" {
		shell = DefaultShell
	}
	// El segundo campo es la contrasena; la x remite a /etc/shadow.
	return fmt.Sprintf("%s:x:%d:%d:%s:%s:%s", u.Name, u.UID, u.GID, u.Gecos, u.Home, shell), nil
}

// Shadow devuelve la linea de /etc/shadow.
//
// El tercer campo son los dias desde 1970 en que se cambio la contrasena, que es
// lo que usa chage. Los demas son los valores de siempre: sin caducidad forzada,
// aviso siete dias antes, cuenta que no expira.
func (u User) Shadow(now time.Time) (string, error) {
	if err := u.Validate(); err != nil {
		return "", err
	}
	// /etc/shadow no admite una contrasena en claro: solo un hash.
	if !strings.HasPrefix(u.Password, "$6$") {
		return "", fmt.Errorf("%w: la contrasena no es un hash SHA-512", ErrInvalidUser)
	}
	days := int(now.Unix() / 86400)
	return fmt.Sprintf("%s:%s:%d:0:99999:7:::", u.Name, u.Password, days), nil
}

// Hash calcula el hash que va en /etc/shadow a partir de la contrasena.
//
// Usa SHA-512 crypt ($6$) con sal aleatoria, que es lo que entiende cualquier
// distribucion actual. Mint 22 prefiere yescrypt ($y$), pero lee $6$ sin problema.
func Hash(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("%w: la contrasena esta vacia", ErrInvalidUser)
	}
	hash, err := sha512_crypt.New().Generate([]byte(password), nil)
	if err != nil {
		return "", fmt.Errorf("calcular el hash de la contrasena: %w", err)
	}
	return hash, nil
}

// AddMembers anade usuarios a un grupo que ya existe en el contenido de
// /etc/group.
//
// No crea el grupo: si no esta, devuelve error. Es a proposito, porque crear un
// grupo con un GID inventado puede pisar el de otro paquete. Los usuarios que ya
// eran miembros no se duplican, asi que se puede llamar dos veces sin miedo.
func AddMembers(content, group string, members ...string) (string, error) {
	if group == "" {
		return "", fmt.Errorf("%w: grupo vacio", ErrInvalidUser)
	}

	lines := strings.Split(content, "\n")
	found := false

	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[0] != group {
			continue
		}

		found = true
		list := fields[3]
		current := make(map[string]bool)
		for _, m := range strings.Split(list, ",") {
			if m != "" {
				current[m] = true
			}
		}
		for _, m := range members {
			if m == "" || current[m] {
				continue
			}
			if list == "" {
				list = m
			} else {
				list += "," + m
			}
			current[m] = true
		}
		fields[3] = list
		lines[i] = strings.Join(fields, ":")
	}

	if !found {
		return "", fmt.Errorf("el grupo %q no existe en /etc/group", group)
	}
	return strings.Join(lines, "\n"), nil
}
