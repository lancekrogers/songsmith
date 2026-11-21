package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/machines"
)

// demoTailnetEnv names a JSON file of computers that stands in for the
// tailnet while the demo tapes record, so Find on Tailscale and
// machines add --discover never ask the real Tailscale. Every address in it
// must be on loopback. Unset, it does nothing.
const demoTailnetEnv = "SONGSMITH_DEMO_TAILNET"

type demoComputer struct {
	Name    string   `json:"name"`
	OS      string   `json:"os"`
	Online  bool     `json:"online"`
	Address string   `json:"address"`
	URLs    []string `json:"urls"`
}

// demoFinder lists the computers in the demo file, or is nil when the
// variable is unset.
func demoFinder() (machines.Finder, error) {
	path := os.Getenv(demoTailnetEnv)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", demoTailnetEnv, err)
	}
	var list []demoComputer
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %s is not a list of computers: %w", demoTailnetEnv, path, err)
	}
	var cs []machines.Candidate
	for _, d := range list {
		if d.Name == "" || d.Address == "" || len(d.URLs) == 0 {
			return nil, fmt.Errorf("%s: every computer in %s needs a name, an address and at least one url", demoTailnetEnv, path)
		}
		c := machines.Candidate{Name: d.Name, OS: d.OS, Online: d.Online, Address: d.Address, Hosts: []string{d.Address}}
		for _, raw := range d.URLs {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) {
				return nil, fmt.Errorf("%s: %s must be an http://127.0.0.1 address, not %q", demoTailnetEnv, d.Name, raw)
			}
			norm, err := config.NormalizeURL(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", demoTailnetEnv, d.Name, err)
			}
			c.URLs = append(c.URLs, norm)
		}
		cs = append(cs, c)
	}
	return func(context.Context) ([]machines.Candidate, error) { return slices.Clone(cs), nil }, nil
}

// machineFinder is how machines add --discover looks for computers: the one
// set on a, else the demo file's, else Tailscale.
func (a *app) machineFinder(cfg config.Config) (machines.Finder, error) {
	if a.findMachines != nil {
		return a.findMachines, nil
	}
	if f, err := demoFinder(); f != nil || err != nil {
		return f, err
	}
	return machines.TailscaleFinder(cfg.ComfyUI.Port), nil
}
