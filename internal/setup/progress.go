// Package setup installs what songsmith needs to render: ComfyUI, the
// MiniMax Music 3 model files and the Ollama writer model.
package setup

import (
	"fmt"
	"time"
)

type Phase string

const (
	PhaseStart    Phase = "start"
	PhaseDownload Phase = "download"
	PhaseVerify   Phase = "verify"
	PhaseInstall  Phase = "install"
	PhaseDone     Phase = "done"
	PhaseSkipped  Phase = "skipped"
)

// Progress is one tick from an installer. Bytes and Total are 0 when the
// phase has no byte count; Percent is always within [0, 100].
type Progress struct {
	Asset   string
	Step    string
	Phase   Phase
	Bytes   int64
	Total   int64
	Percent float64
	Detail  string
}

func newProgress(asset string, phase Phase, bytes, total int64, detail string) Progress {
	p := Progress{Asset: asset, Phase: phase, Bytes: bytes, Total: max(total, 0), Detail: detail}
	if p.Total > 0 {
		p.Percent = min(max(float64(bytes)/float64(p.Total)*100, 0), 100)
	}
	if phase == PhaseDone || phase == PhaseSkipped {
		p.Percent = 100
	}
	return p
}

// throttle forwards download ticks at most every interval; every other
// phase passes straight through, so first and last ticks are never dropped.
func throttle(fn func(Progress), interval time.Duration) func(Progress) {
	if fn == nil {
		return func(Progress) {}
	}
	var last time.Time
	return func(p Progress) {
		if p.Phase == PhaseDownload {
			if time.Since(last) < interval {
				return
			}
			last = time.Now()
		} else {
			last = time.Time{}
		}
		fn(p)
	}
}

// GB formats a byte count as gigabytes with one decimal (two below 1 GB).
func GB(n int64) string {
	if n < 1e9 {
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}
