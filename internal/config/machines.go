package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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
// URL songsmith talks to: http:// is added when no scheme is given, the
// scheme's default port and a trailing slash are dropped, and an IP address
// is written the one way, so one server has one address.
func NormalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("the address is empty; give the machine's ComfyUI address, such as http://studio.tailnet.ts.net:8188")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(escapeZone(s))
	if err != nil {
		return "", fmt.Errorf("%q is not an address songsmith can use: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q starts with %s://; a ComfyUI address starts with http:// or https://", raw, u.Scheme)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return "", fmt.Errorf("%q has no host name; give it as http://<host>:<port>, such as http://studio.tailnet.ts.net:8188", raw)
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("%q has no port after the colon; add the port, such as :8188, or drop the colon", raw)
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || port[0] == '0' {
			return "", fmt.Errorf("%q has port %s; a port is a number from 1 to 65535, written without a leading 0", raw, port)
		}
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%q has more than a host and port; give only the server's address, such as http://%s", raw, u.Host)
	}
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	}
	hostPort := host
	switch {
	case port != "":
		hostPort = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		hostPort = "[" + host + "]"
	}
	out := (&url.URL{Scheme: u.Scheme, Host: hostPort}).String()
	if back, err := url.Parse(out); err != nil || back.Host != hostPort {
		return "", fmt.Errorf("%q is not an address songsmith can use; give it as http://<host>:<port>, such as http://studio.tailnet.ts.net:8188", raw)
	}
	return out, nil
}

// escapeZone lets an IPv6 zone be typed as it is written elsewhere, such as
// [fe80::1%en0], where a URL needs [fe80::1%25en0].
func escapeZone(s string) string {
	open, end := strings.Index(s, "["), strings.Index(s, "]")
	if open < 0 || end < open {
		return s
	}
	i := strings.Index(s[open:end], "%")
	if i < 0 || strings.HasPrefix(s[open+i:end], "%25") {
		return s
	}
	return s[:open+i] + "%25" + s[open+i+1:]
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
	case strings.ContainsFunc(n, unicode.IsControl):
		return fmt.Errorf("the machine name %q has a tab, line break or other control character; use letters, numbers, spaces and punctuation", name)
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

// isLocalServer reports whether base reaches the ComfyUI server described by
// local. A server listening on every address (0.0.0.0 or ::) is reached on
// this computer's loopback addresses and its own network addresses too.
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
	if strings.EqualFold(host, local.Host) || sameIP(host, local.Host) {
		return true
	}
	if ownAddress(host) && ownAddress(local.Host) {
		return true
	}
	return everyAddress(local.Host) && interfaceAddress(host)
}

func sameIP(a, b string) bool {
	x, errX := netip.ParseAddr(a)
	y, errY := netip.ParseAddr(b)
	return errX == nil && errY == nil && x.Unmap() == y.Unmap()
}

// ownAddress reports whether host, dialed on this computer, reaches this
// computer: localhost, a loopback address, or the any-address 0.0.0.0 or ::.
func ownAddress(host string) bool {
	return loopback(host) || everyAddress(host)
}

func everyAddress(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// interfaceAddress reports whether host is an IP address of one of this
// computer's network interfaces.
func interfaceAddress(host string) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().WithZone("") == ip.WithZone("").Unmap() {
			return true
		}
	}
	return false
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

// TakesNewSongs reports whether this computer or any added machine is sent
// new songs.
func (c Config) TakesNewSongs() bool {
	return c.Local.Enabled || slices.ContainsFunc(c.Machines, func(m Machine) bool { return m.Enabled })
}

// ErrOnlyMachine is why a change that would leave no machine to send new
// songs to is refused.
var ErrOnlyMachine = errors.New("the only machine that renders new songs")

func onlyMachine(name string) error {
	return fmt.Errorf("%s is %w; turn on another one first, so new songs have somewhere to go", name, ErrOnlyMachine)
}

// AddMachine adds m to the machines in the config file at path as the file
// is now, and returns it as saved.
func AddMachine(path string, m Machine) (Machine, error) {
	var added Machine
	err := update(path, func(cur Config) (string, any, error) {
		ms, err := cur.WithMachine(m)
		if err != nil {
			return "", nil, err
		}
		added = ms[len(ms)-1]
		return "machines", ms, nil
	})
	return added, err
}

// RemoveMachine removes the machine called name from the config file at
// path, unless it is the last one that renders new songs, and returns it.
func RemoveMachine(path, name string) (Machine, error) {
	var removed Machine
	err := update(path, func(cur Config) (string, any, error) {
		ms, err := cur.WithoutMachine(name)
		if err != nil {
			return "", nil, err
		}
		removed, _ = cur.Machine(name)
		after := cur
		after.Machines = ms
		if cur.TakesNewSongs() && !after.TakesNewSongs() {
			return "", nil, onlyMachine(removed.Name)
		}
		return "machines", ms, nil
	})
	return removed, err
}

// SetMachineEnabled turns sending new songs to the machine called name, or
// to this computer, on or off in the config file at path. It refuses to
// turn off the last one that renders new songs.
func SetMachineEnabled(path, name string, on bool) error {
	return update(path, func(cur Config) (string, any, error) {
		after := cur
		key, value, label := "local.enabled", any(on), ThisComputer
		if strings.EqualFold(name, ThisComputer) {
			after.Local.Enabled = on
		} else {
			i := slices.IndexFunc(cur.Machines, func(m Machine) bool { return strings.EqualFold(m.Name, name) })
			if i < 0 {
				return "", nil, fmt.Errorf("there is no machine named %q", name)
			}
			after.Machines = slices.Clone(cur.Machines)
			after.Machines[i].Enabled = on
			key, value, label = "machines", after.Machines, after.Machines[i].Name
		}
		if cur.TakesNewSongs() && !after.TakesNewSongs() {
			return "", nil, onlyMachine(label)
		}
		return key, value, nil
	})
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
