// Package machines reaches the computers songsmith renders on: this one, and
// ComfyUI servers on others, which it checks and keeps render speeds for.
package machines

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/backend"
	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/health"
)

// State sums up whether a machine can render.
type State string

const (
	Ready         State = "ready"
	Offline       State = "offline"
	ModelsMissing State = "models missing"
	TooOld        State = "ComfyUI too old"
)

// Status is what a check found on a machine.
type Status struct {
	State State
	// Detail says what was found, and for a machine that is not ready,
	// what to do about it.
	Detail string
	// Missing names the nodes a machine that is too old lacks, or the model
	// files a machine with models missing cannot see.
	Missing []string
	// Reason is, in a few words, why a machine that is offline did not
	// answer, such as "connection refused".
	Reason  string
	Memory  int64
	Device  string
	Version string
}

// CheckTimeout bounds one check of a machine.
const CheckTimeout = 10 * time.Second

// Check asks the ComfyUI server at baseURL whether it can render with the
// model files models names: that it answers, has the MiniMax Music 3 nodes
// and lists the files.
func Check(ctx context.Context, baseURL string, models config.ComfyUI) Status {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	c := comfy.NewClient(baseURL)
	stats, err := c.SystemStats(ctx)
	if err != nil {
		return offline(baseURL, err)
	}
	st := Status{Memory: stats.Memory(), Version: stats.System.ComfyUIVersion}
	if len(stats.Devices) > 0 {
		st.Device = stats.Devices[0].Type
	}
	nodes, files, err := health.MissingNodes(ctx, c, models)
	switch {
	case err != nil:
		return offline(baseURL, err)
	case len(nodes) > 0:
		st.State, st.Missing = TooOld, nodes
		st.Detail = fmt.Sprintf("ComfyUI at %s lacks %s. Update ComfyUI on that computer to a version with MiniMax Music 3 support.",
			baseURL, strings.Join(nodes, ", "))
	case len(files) > 0:
		st.State, st.Missing = ModelsMissing, files
		st.Detail = fmt.Sprintf("ComfyUI at %s cannot see %s. Install the MiniMax Music 3 files on that computer (`songsmith setup --install` there) "+
			"or set this machine's model file names, then restart ComfyUI there.", baseURL, strings.Join(files, ", "))
	default:
		st.State = Ready
		st.Detail = "ready at " + baseURL + st.about()
	}
	return st
}

func offline(baseURL string, err error) Status {
	detail := fmt.Sprintf("not answering at %s (%v). Check that the computer is awake, that ComfyUI runs there (`songsmith server start` on that computer), "+
		"and that its comfyui.host is an address this computer can reach.", baseURL, err)
	reason := err.Error()
	if i := strings.LastIndex(reason, ": "); i >= 0 {
		reason = reason[i+2:]
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = fmt.Sprintf("no answer within %s", CheckTimeout)
	}
	if !errors.Is(err, comfy.ErrUnreachable) && !errors.Is(err, context.DeadlineExceeded) {
		detail = fmt.Sprintf("ComfyUI at %s answered with an error: %v", baseURL, err)
		reason = "answered with an error: " + err.Error()
	}
	return Status{State: Offline, Detail: detail, Reason: reason}
}

// about describes the hardware a check found, such as ", mps, 64 GB, ComfyUI 0.3.60".
func (s Status) about() string {
	var parts []string
	if s.Device != "" {
		parts = append(parts, s.Device)
	}
	if s.Memory > 0 {
		parts = append(parts, backend.GB(s.Memory))
	}
	if s.Version != "" {
		parts = append(parts, "ComfyUI "+s.Version)
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

// Backend renders on the added machine called name, at baseURL. Each render
// reads get once, with the model file names that machine sets in place of
// the ones under comfyui:. It has no starter: songsmith never starts a
// server on another computer.
func Backend(name, baseURL string, get func() config.Config) *backend.ComfyMiniMax {
	return backend.NewComfyMiniMax(comfy.NewClient(baseURL), nil, func() config.Config {
		cfg := get()
		if m, ok := cfg.Machine(name); ok {
			cfg.ComfyUI = m.Models(cfg.ComfyUI)
		}
		return cfg
	})
}

// PingTimeout bounds the check that a machine answers before a song is sent to it.
const PingTimeout = 5 * time.Second

// Ping reports whether the ComfyUI server at baseURL answers.
func Ping(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	_, err := comfy.NewClient(baseURL).SystemStats(ctx)
	return err
}
