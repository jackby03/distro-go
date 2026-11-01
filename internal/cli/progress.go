package cli

import (
	"fmt"
	"strings"
	"time"
)

// progressWidth es el ancho al que se rellena la linea de avance.
//
// Sin relleno, cuando la linea nueva es mas corta que la anterior quedan restos de
// la vieja en pantalla, porque solo se repinta con \r.
const progressWidth = 150

// progressLine monta una linea de avance: hora, avance, ritmo, tiempo restante y un
// detalle opcional.
//
// Los textos llegan ya formateados porque el volcado cuenta entradas y el flasheo
// cuenta bytes: lo unico comun es la forma de la linea.
func progressLine(hecho, total, ritmo, restante, detalle string) string {
	linea := fmt.Sprintf("[%s] %s / %s · %s · quedan %s",
		time.Now().Format("15:04:05"), hecho, total, ritmo, restante)
	if detalle != "" {
		linea += " · " + detalle
	}
	return "\r" + padRight(linea, progressWidth)
}

// etaText calcula cuanto falta a partir del ritmo y de lo que queda por hacer.
// Devuelve "-" cuando no hay datos suficientes para estimar.
func etaText(rate, falta float64) string {
	if rate <= 0 || falta <= 0 {
		return "-"
	}
	return time.Duration(falta / rate * float64(time.Second)).Round(time.Second).String()
}

// padRight rellena con espacios hasta el ancho pedido, sin recortar.
func padRight(s string, ancho int) string {
	if len(s) >= ancho {
		return s
	}
	return s + strings.Repeat(" ", ancho-len(s))
}
