# AGENTS.md — DistroGo Protocol & Engineering Standards

## 1. Project Context & Philosophy
DistroGo es una herramienta de sistemas nativa para Windows (CLI inicialmente) para instalar distribuciones Linux de forma persistente y bare-metal en unidades externas desde una ISO estándar, sin depender de WSL, Hyper-V ni VirtualBox.

### Principios Fundamentales:
- **Zero Kernel Drivers:** No se usan controladores de kernel no firmados en Windows. Todo el manejo de ext4 y SquashFS ocurre en espacio de usuario.
- **Hardware Safety First:** Prohibido escribir o modificar `\\.\PhysicalDrive` durante el ciclo de desarrollo regular. Todo el pipeline se desarrolla y valida contra archivos de imagen plana (`test.img`).
- **UNIX Philosophy:** Componentes desacoplados en paquetes independientes con responsabilidades únicas (`pkg/windisk`, `pkg/squashfs`, `pkg/ext4fs`).

---

## 2. Interaction Protocol (Human-in-the-Loop & Stop-and-Wait)
El agente de IA actúa como copiloto técnico e implementador; el desarrollador humano actúa como Lead Engineer, revisor de seguridad y validador de tests.

1. **Un solo paso a la vez:** Implementa o modifica **un único archivo lógico** por respuesta.
2. **Ciclo obligatorio:**
   - Explicar brevemente el propósito del archivo o función.
   - Presentar el código limpio y completo (sin placeholders del tipo `// TODO: implement later`).
   - Entregar el comando de compilación o test exacto.
   - **DETENERSE.** No escribir código del siguiente paso, ni predecir respuestas, ni encadenar tareas.
3. **Punto de parada obligatorio:** Toda respuesta con código debe terminar solicitando explícitamente al humano la validación de las pruebas antes de continuar.
4. **Prohibición de autonomía:** Queda estrictamente prohibido avanzar a un siguiente submódulo o hito sin la confirmación explícita del humano ("aprobado", "listo", "adelante").

---

## 3. Tech Stack & Environment
- **Lenguaje:** Go (última versión estable, arquitectura `windows/amd64`).
- **CGO:** Estrictamente deshabilitado (`CGO_ENABLED=0`). Binario estático puro.
- **Librerías Core:** `golang.org/x/sys/windows` para llamadas a APIs de Win32/NTFS.
- **Banco de Pruebas:** QEMU para Windows + Alpine ISO / OVMF para validar imágenes `test.img`.

---

## 4. Coding Standards & Performance Rules
- **Resource Management:**
  - NUNCA usar `defer` dentro de bucles o funciones callback de recorrido (`WalkDir`, iteradores de inodos). Cerrar descriptores explícitamente tras consumir cada archivo.
  - Streaming constante con buffer acotado (máximo 1 MiB por lectura/escritura con `io.CopyBuffer`). Prohibido leer archivos completos del rootfs a memoria (`os.ReadFile` / `io.ReadAll`).
- **Error Handling:**
  - Todos los errores deben ser contextualizados usando `fmt.Errorf("operación fallida en [contexto]: %w", err)`.
  - Prohibido el uso de `panic()` fuera de inicializaciones estáticas o guardias de seguridad de hardware.
- **POSIX Fidelity:**
  - Respetar UID, GID, modos POSIX (`0755`, `4755` setuid), symlinks, inodos y mtime en los contratos de datos, sin depender de `os.FileInfo.Sys()` en Windows.
  - El ajuste de permisos (`Chmod`) y tiempos (`SetTimes`) debe ejecutarse en una segunda pasada posterior al volcado de datos para preservar bits especiales.

---

## 5. Verification Commands
- **Verificación de sintaxis y tipos:**
```powershell
  go vet ./...

```

* **Ejecución de tests unitarios:**
```powershell
go test -v -race ./pkg/...

```


* **Validación del arnés QEMU (Alpine Guest):**
```powershell
qemu-system-x86_64 -m 2G -smp 2 -nographic -serial mon:stdio -drive file=alpine-virt-3.24.3-x86_64.iso,media=cdrom -drive file=test.img,format=raw,if=virtio

```

---

## 6. Definition of Done (DoD)

Un hito o submódulo solo se considera terminado cuando:

1. El código compila limpiamente con `CGO_ENABLED=0`.
2. Los tests unitarios del paquete pasan al 100%.
3. El comando de validación en QEMU reporta un resultado coherente (`e2fsck` limpio o parseo verificado).
4. El humano otorga explícitamente el visto bueno para la siguiente tarea.

