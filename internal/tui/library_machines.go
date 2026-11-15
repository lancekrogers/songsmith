package tui

import (
	"cmp"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
)

// machinesInUse reports whether there is anything to say about machines:
// some are added, or the song renders on or is pinned to one. Without
// that the library reads as it did before machines.
func (a *app) machinesInUse(s library.Song) bool {
	return len(a.cfg().Machines) > 0 || s.RenderingOn != "" || s.Machine != ""
}

// lostMachine reports whether the machine holding the song's render has
// stopped answering, and which machine that is.
func lostMachine(s library.Song) (string, bool) {
	held := s.Status == library.StatusRendering || s.Status == library.StatusQueued && s.JobID != ""
	return cmp.Or(s.RenderingOn, config.ThisComputer), held && !s.LostSince.IsZero()
}

// noMachine reports whether no machine can take a queued song even once
// one is free. machine is the one it is set to render on, which is turned
// off or, when gone, no longer added; it is "" when every machine is off.
func (a *app) noMachine(s library.Song) (machine string, gone, stuck bool) {
	if s.Status != library.StatusQueued || s.JobID != "" {
		return "", false, false
	}
	cfg := a.cfg()
	if s.Machine == "" {
		return "", false, !cfg.TakesNewSongs()
	}
	if strings.EqualFold(s.Machine, config.ThisComputer) {
		return config.ThisComputer, false, !cfg.Local.Enabled
	}
	m, ok := cfg.Machine(s.Machine)
	if !ok {
		return s.Machine, true, true
	}
	return m.Name, false, !m.Enabled
}

// noMachineNote says, for the launcher, that no machine takes new songs;
// "" when one does.
func (a *app) noMachineNote() string {
	if a.cfg().TakesNewSongs() {
		return ""
	}
	return "Every machine is turned off; songs wait until one is on (Settings → Machines)"
}

// whereLine says, in a few words, where a busy song renders or what it
// waits for, for its line in the list; "" when there is nothing to add.
func (a *app) whereLine(s library.Song) string {
	if machine, gone, stuck := a.noMachine(s); stuck {
		switch {
		case machine == "":
			return "waits: every machine is turned off"
		case gone:
			return "waits: " + machine + " is not one of the machines"
		}
		return "waits: " + machine + " is turned off"
	}
	if !a.machinesInUse(s) {
		return ""
	}
	on := cmp.Or(s.RenderingOn, config.ThisComputer)
	if name, lost := lostMachine(s); lost {
		return "waiting for " + name + " to come back"
	}
	switch {
	case s.Status == library.StatusRendering:
		return "on " + on + " · " + progressLabel(a.library.progress[s.ID])
	case s.Status == library.StatusQueued && s.JobID != "":
		return "picking up its render on " + on
	case s.Status == library.StatusQueued && s.Machine != "":
		return "waits for " + s.Machine
	}
	return ""
}

// whereText says, for the detail pane, where the song renders, what it
// waits for, or the machine it is set to render on.
func (a *app) whereText(s library.Song) string {
	if machine, gone, stuck := a.noMachine(s); stuck {
		switch {
		case machine == "":
			return "Waits: every machine is turned off. Turn one on in Settings → Machines."
		case gone:
			return "Waits for " + machine + ", the machine it is set to render on, which is not one of the machines. Choose another machine for it."
		}
		return "Waits for " + machine + ", the machine it is set to render on, which is turned off. " +
			"Turn it on in Settings → Machines, or choose another machine for it."
	}
	if !a.machinesInUse(s) {
		return ""
	}
	on := cmp.Or(s.RenderingOn, config.ThisComputer)
	if name, lost := lostMachine(s); lost {
		return "Waiting for " + name + " to come back. Its render keeps going there, and songsmith picks it up when " + name +
			" answers, or queues the song again after " + shortDuration(a.cfg().ReclaimAfter) + "."
	}
	switch {
	case s.Status == library.StatusRendering:
		return "Rendering on " + on
	case s.Status == library.StatusQueued && s.JobID != "":
		return "Picking up its render on " + on
	case s.Status == library.StatusQueued && s.Machine != "":
		return "Waits for " + s.Machine + ", the machine it is set to render on"
	case s.Status == library.StatusQueued:
		return "Goes to the first free machine"
	case s.Machine != "":
		return "Set to render on " + s.Machine
	}
	return ""
}

// shortDuration writes d as 10m or 1h30m rather than 10m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// takeWhere says where a take was made: the machine a render ran on, or
// for a master the machine its source was rendered on. local names this
// computer, which is said only once machines are added.
func takeWhere(takes []library.Take, t library.Take, local bool) string {
	machine := t.Machine
	prefix := "on "
	if t.Kind == library.TakeMaster {
		i := library.TakeIndex(takes, t.Source)
		if i == 0 {
			return ""
		}
		machine, prefix = takes[i-1].Machine, "from a render on "
	}
	if machine == "" && !local {
		return ""
	}
	return prefix + cmp.Or(machine, config.ThisComputer)
}
