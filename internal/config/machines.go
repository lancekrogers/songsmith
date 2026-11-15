package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ThisComputer is the name of the machine songsmith runs on, the one whose
// ComfyUI server the comfyui: section describes. No added machine may use it.
const ThisComputer = "This computer"

// Machine is a ComfyUI server on another computer that renders songs.
// Songsmith only sends it work: it never starts, stops or installs to it.
type Machine struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// Enabled machines are sent new songs; Load turns a missing key on.
	Enabled bool `yaml:"enabled"`
	// The model file names on that machine; empty means the names under comfyui:.
	DiffusionModel string `yaml:"diffusion_model,omitempty"`
	TextEncoder    string `yaml:"text_encoder,omitempty"`
	VAE            string `yaml:"vae,omitempty"`
}

func (m *Machine) UnmarshalYAML(n *yaml.Node) error {
	type plain Machine
	p := plain{Enabled: true}
	if err := n.Decode(&p); err != nil {
		return err
	}
	*m = Machine(p)
	return nil
}

// Models returns c with the model file names m sets in place of c's.
func (m Machine) Models(c ComfyUI) ComfyUI {
	if m.DiffusionModel != "" {
		c.DiffusionModel = m.DiffusionModel
	}
	if m.TextEncoder != "" {
		c.TextEncoder = m.TextEncoder
	}
	if m.VAE != "" {
		c.VAE = m.VAE
	}
	return c
}

// Local is how songsmith uses the ComfyUI server on this computer.
type Local struct {
	// Enabled renders songs on this computer as well as on added machines.
	Enabled bool `yaml:"enabled"`
}

// DefaultReclaimAfter is how long a song waits for a machine that stopped
// answering mid-render before it is queued again.
const DefaultReclaimAfter = 10 * time.Minute

// Machine returns the added machine called name, ignoring case.
func (c Config) Machine(name string) (Machine, bool) {
	for _, m := range c.Machines {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Machine{}, false
}

// NormalizeURL turns what a user typed for a machine's address into the base
// URL songsmith talks to: http:// is added when no scheme is given, and a
// trailing slash is dropped.
func NormalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("the address is empty; give the machine's ComfyUI address, such as http://studio.tailnet.ts.net:8188")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%q is not an address songsmith can use: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q starts with %s://; a ComfyUI address starts with http:// or https://", raw, u.Scheme)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("%q has no host name; give it as http://<host>:<port>, such as http://studio.tailnet.ts.net:8188", raw)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("%q has port %s; a port is a number from 1 to 65535", raw, p)
		}
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%q has more than a host and port; give only the server's address, such as http://%s", raw, u.Host)
	}
	return u.Scheme + "://" + u.Host, nil
}

// CheckMachineName reports why name cannot name an added machine.
func CheckMachineName(name string) error {
	switch n := strings.TrimSpace(name); {
	case n == "":
		return errors.New("a machine needs a name, such as the computer's name")
	case n != name:
		return fmt.Errorf("the machine name %q starts or ends with a space; remove it", name)
	case strings.EqualFold(n, ThisComputer):
		return fmt.Errorf("%q is the name songsmith uses for the computer it runs on; give the machine its own name", name)
	}
	return nil
}

// checkMachines reports every problem with the machines list, and fills in
// each machine's normalized URL.
func checkMachines(local ComfyUI, ms []Machine) error {
	var errs []error
	names := map[string]string{}
	urls := map[string]string{}
	for i := range ms {
		m := &ms[i]
		label := fmt.Sprintf("machines[%d]", i)
		if m.Name != "" {
			label = fmt.Sprintf("machine %q", m.Name)
		}
		if err := CheckMachineName(m.Name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		} else if prev, ok := names[strings.ToLower(m.Name)]; ok {
			errs = append(errs, fmt.Errorf("%s: two machines are named %q and %q; give each its own name", label, prev, m.Name))
		} else {
			names[strings.ToLower(m.Name)] = m.Name
		}
		u, err := NormalizeURL(m.URL)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			continue
		}
		m.URL = u
		if isLocalServer(local, u) {
			errs = append(errs, fmt.Errorf("%s: %s is this computer's own ComfyUI server (comfyui: host and port); it renders already, so it is not added as a machine", label, u))
			continue
		}
		key := strings.ToLower(u)
		if prev, ok := urls[key]; ok {
			errs = append(errs, fmt.Errorf("%s: %s is also the address of machine %q; one server renders one song at a time, so add it once", label, u, prev))
			continue
		}
		urls[key] = m.Name
	}
	return errors.Join(errs...)
}

// isLocalServer reports whether base reaches the ComfyUI server described by local.
func isLocalServer(local ComfyUI, base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if port != strconv.Itoa(local.Port) {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, local.Host) {
		return true
	}
	return loopback(host) && loopback(local.Host)
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WithMachine returns c's machines with m added, its URL normalized, or why
// it cannot be added.
func (c Config) WithMachine(m Machine) ([]Machine, error) {
	if _, ok := c.Machine(m.Name); ok {
		return nil, fmt.Errorf("there is already a machine named %q; remove it first or give this one another name", m.Name)
	}
	ms := append(slices.Clone(c.Machines), m)
	if err := checkMachines(c.ComfyUI, ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// WithoutMachine returns c's machines without the one called name.
func (c Config) WithoutMachine(name string) ([]Machine, error) {
	i := slices.IndexFunc(c.Machines, func(m Machine) bool { return strings.EqualFold(m.Name, name) })
	if i < 0 {
		return nil, fmt.Errorf("there is no machine named %q", name)
	}
	return slices.Delete(slices.Clone(c.Machines), i, i+1), nil
}

// validate reports settings that cannot be used, normalizing machine URLs.
func (c *Config) validate() error {
	var errs []error
	if err := checkMachines(c.ComfyUI, c.Machines); err != nil {
		errs = append(errs, err)
	}
	if c.ReclaimAfter <= 0 {
		errs = append(errs, fmt.Errorf("reclaim_after is %s; give a length of time such as 10m", c.ReclaimAfter))
	}
	return errors.Join(errs...)
}
