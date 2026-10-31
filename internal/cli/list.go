package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"distrogo/pkg/blockdev"
	"distrogo/pkg/safety"
	"distrogo/pkg/windisk"
)

// ------------------------------------------------------------- salida humana

func (a *App) writeHuman(inv windisk.Inventory, verdicts []safety.Verdict) {
	if len(inv.Disks) == 0 {
		fmt.Fprintln(a.Out, "no se encontro ningun disco")
		return
	}

	byNumber := make(map[uint32]safety.Verdict, len(verdicts))
	for _, v := range verdicts {
		byNumber[v.DiskNumber] = v
	}

	fmt.Fprintf(a.Out, "%d discos\n", len(inv.Disks))
	for _, d := range inv.Disks {
		v := byNumber[d.Number]

		fmt.Fprintf(a.Out, "\nDisco %d  %s  [%s]\n", d.Number, orDash(d.Model), d.Bus)
		fmt.Fprintf(a.Out, "  serial   : %s\n", orDash(d.Serial))
		fmt.Fprintf(a.Out, "  tamano   : %s\n", blockdev.HumanSize(d.SizeBytes))
		fmt.Fprintf(a.Out, "  sectores : %d logicos / %d fisicos\n", d.LogicalSector, d.PhysicalSector)
		fmt.Fprintf(a.Out, "  estado   : %s\n", estado(v))
		for _, r := range v.Reasons {
			fmt.Fprintf(a.Out, "  motivo   : %s\n", r.Detail)
		}
		for _, w := range v.Warnings {
			fmt.Fprintf(a.Out, "  aviso    : %s\n", w.Detail)
		}

		if len(d.Partitions) == 0 {
			fmt.Fprintln(a.Out, "  particiones: ninguna")
		} else {
			fmt.Fprintln(a.Out, "  particiones:")
			for _, p := range d.Partitions {
				fmt.Fprintf(a.Out, "    %-3d %10s  %s\n", p.Number, blockdev.HumanSize(p.SizeBytes), partitionExtra(p))
			}
		}
	}

	for _, w := range inv.Warnings {
		fmt.Fprintf(a.Err, "aviso: %v\n", w)
	}
}

func estado(v safety.Verdict) string {
	switch {
	case !v.Eligible:
		return "BLOQUEADO"
	case v.Destructive:
		return "ELEGIBLE (destructivo)"
	default:
		return "ELEGIBLE"
	}
}

func partitionExtra(p blockdev.Partition) string {
	var parts []string
	if p.Volume != nil && p.Volume.Mounted() {
		parts = append(parts, "montada en "+p.Volume.MountPoint)
		if p.Volume.Label != "" {
			parts = append(parts, fmt.Sprintf("%q", p.Volume.Label))
		}
		if p.Volume.FileSystem != "" {
			parts = append(parts, p.Volume.FileSystem)
		}
	} else {
		parts = append(parts, "sin montar")
	}
	if p.TypeGUID != "" {
		parts = append(parts, p.TypeGUID)
	}
	return strings.Join(parts, "  ")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// ------------------------------------------------------------------- salida JSON

type jsonReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type jsonPartition struct {
	Number     uint32 `json:"number"`
	Offset     int64  `json:"offset"`
	SizeBytes  int64  `json:"size_bytes"`
	TypeGUID   string `json:"type_guid,omitempty"`
	MountPoint string `json:"mount_point,omitempty"`
	Label      string `json:"label,omitempty"`
	FileSystem string `json:"file_system,omitempty"`
}

type jsonDisk struct {
	Number         uint32          `json:"number"`
	Model          string          `json:"model"`
	Serial         string          `json:"serial"`
	Bus            string          `json:"bus"`
	SizeBytes      int64           `json:"size_bytes"`
	LogicalSector  int             `json:"logical_sector"`
	PhysicalSector int             `json:"physical_sector"`
	Removable      bool            `json:"removable"`
	Eligible       bool            `json:"eligible"`
	Destructive    bool            `json:"destructive"`
	Reasons        []jsonReason    `json:"reasons,omitempty"`
	Warnings       []jsonReason    `json:"warnings,omitempty"`
	Partitions     []jsonPartition `json:"partitions,omitempty"`
}

type jsonReport struct {
	Disks    []jsonDisk `json:"disks"`
	Warnings []string   `json:"warnings,omitempty"`
}

func (a *App) writeJSON(inv windisk.Inventory, verdicts []safety.Verdict) error {
	byNumber := make(map[uint32]safety.Verdict, len(verdicts))
	for _, v := range verdicts {
		byNumber[v.DiskNumber] = v
	}

	report := jsonReport{Disks: make([]jsonDisk, 0, len(inv.Disks))}
	for _, d := range inv.Disks {
		v := byNumber[d.Number]
		jd := jsonDisk{
			Number:         d.Number,
			Model:          d.Model,
			Serial:         d.Serial,
			Bus:            d.Bus.String(),
			SizeBytes:      d.SizeBytes,
			LogicalSector:  d.LogicalSector,
			PhysicalSector: d.PhysicalSector,
			Removable:      d.Removable,
			Eligible:       v.Eligible,
			Destructive:    v.Destructive,
			Reasons:        toJSONReasons(v.Reasons),
			Warnings:       toJSONReasons(v.Warnings),
		}
		for _, p := range d.Partitions {
			jp := jsonPartition{
				Number:    p.Number,
				Offset:    p.Offset,
				SizeBytes: p.SizeBytes,
				TypeGUID:  p.TypeGUID,
			}
			if p.Volume != nil {
				jp.MountPoint = p.Volume.MountPoint
				jp.Label = p.Volume.Label
				jp.FileSystem = p.Volume.FileSystem
			}
			jd.Partitions = append(jd.Partitions, jp)
		}
		report.Disks = append(report.Disks, jd)
	}
	for _, w := range inv.Warnings {
		report.Warnings = append(report.Warnings, w.Error())
	}

	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func toJSONReasons(reasons []safety.Reason) []jsonReason {
	if len(reasons) == 0 {
		return nil
	}
	out := make([]jsonReason, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, jsonReason{Code: r.Code, Detail: r.Detail})
	}
	return out
}
