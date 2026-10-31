---
name: thermos
description: "Doble revisión thermo-nuclear (bugs/seguridad + calidad de código) en un solo pase, con síntesis priorizada. Úsala para 'thermos', 'doble revisión', auditorías de rama combinadas."
disable-model-invocation: true
---

# Thermos

Adaptación para Pi de `cursor/plugins` (thermos/skills/thermos).
Pi no tiene subagentes: las dos pasadas se ejecutan **en secuencia** sobre el mismo alcance.

## Flujo

1. Determinar el alcance: diff de la rama, PR, o ficheros señalados por el usuario.
2. Reunir el diff y los extractos de contexto necesarios para no revisar a ciegas.
3. Aplicar **en este orden** sobre el mismo alcance:
   1. `thermo-nuclear-review` — bugs, roturas, seguridad, regresiones de devex, feature flags.
   2. `thermo-nuclear-code-quality-review` — mantenibilidad, tamaño de ficheros, espagueti,
      abstracciones, salud del código.
4. Sintetizar:
   - Hallazgos primero, deduplicados entre las dos pasadas.
   - Peso extra a lo que aparece en ambas.
   - Resolver desacuerdos con criterio propio y decirlo.
   - Resumen breve; no repetir los informes enteros.

## Origen

- Upstream: https://github.com/cursor/plugins → `thermos/`
- Licencia: ver `thermos/LICENSE` en el repo upstream.
