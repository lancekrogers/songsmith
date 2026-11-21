package machines

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/tailnet"
)

// DefaultPort is the port ComfyUI listens on unless told otherwise.
const DefaultPort = 8188

// Candidate is a computer that may run ComfyUI, found on the tailnet.
type Candidate struct {
	// Name is what to call it, such as its host name.
	Name   string
	Online bool
	OS     string
	// Address is where it is on the tailnet, such as 100.64.0.7.
	Address string
	// URLs are the addresses its ComfyUI may answer at, in the order they
	// are asked. One that answers at none is saved with the first.
	URLs []string
	// Hosts are every name and address it is known by, to tell whether it
	// was added already under any of them.
	Hosts []string
}

// Finder lists the computers that may run ComfyUI.
type Finder func(ctx context.Context) ([]Candidate, error)

// TailscaleFinder finds the other computers on this computer's tailnet. Each
// is asked on port 8188, then on port when that differs.
func TailscaleFinder(port int) Finder {
	return func(ctx context.Context) ([]Candidate, error) {
		nodes, err := tailnet.Peers(ctx)
		if err != nil {
			return nil, err
		}
		return candidates(nodes, port), nil
	}
}

func candidates(nodes []tailnet.Node, port int) []Candidate {
	ports := []int{DefaultPort}
	if port > 0 && port != DefaultPort {
		ports = append(ports, port)
	}
	out := make([]Candidate, 0, len(nodes))
	for _, n := range nodes {
		c := Candidate{Name: n.Name, Online: n.Online, OS: n.OS, Address: n.Address}
		for _, p := range ports {
			if u, err := config.NormalizeURL("http://" + net.JoinHostPort(n.Address, strconv.Itoa(p))); err == nil {
				c.URLs = append(c.URLs, u)
			}
		}
		for _, h := range append([]string{n.DNSName, n.Short}, n.IPs...) {
			if h != "" {
				c.Hosts = append(c.Hosts, h)
			}
		}
		out = append(out, c)
	}
	return out
}

// AddedAs returns the name of the machine in cfg that is c: one saved at
// one of c's URLs, or at any name or address c is known by, on any port.
func (c Candidate) AddedAs(cfg config.Config) (string, bool) {
	for _, m := range cfg.Machines {
		for _, u := range c.URLs {
			if strings.EqualFold(m.URL, u) {
				return m.Name, true
			}
		}
		parsed, err := url.Parse(m.URL)
		if err != nil {
			continue
		}
		host := strings.TrimSuffix(parsed.Hostname(), ".")
		for _, h := range c.Hosts {
			if sameHost(host, h) {
				return m.Name, true
			}
		}
	}
	return "", false
}

// Matches reports whether s names c: its name, or any name or address it
// is known by, ignoring case.
func (c Candidate) Matches(s string) bool {
	return strings.EqualFold(c.Name, s) || slices.ContainsFunc(c.Hosts, func(h string) bool { return sameHost(strings.TrimSuffix(s, "."), h) })
}

func sameHost(a, b string) bool {
	x, errX := netip.ParseAddr(a)
	y, errY := netip.ParseAddr(b)
	if errX == nil || errY == nil {
		return errX == nil && errY == nil && x.WithZone("").Unmap() == y.WithZone("").Unmap()
	}
	return strings.EqualFold(a, strings.TrimSuffix(b, "."))
}

// Found is what asking one candidate found.
type Found struct {
	// Asked is false for a computer left alone: one already added, or one
	// Tailscale says is offline.
	Asked bool
	// URL is where it would be saved: where its ComfyUI answered, else its
	// first URL.
	URL string
	// Answered is set when ComfyUI answered at URL; Status is then what its
	// check found.
	Answered bool
	Status   Status
	// Reason is, in a few words, why nothing answered.
	Reason string
}

// AskTimeout bounds asking one address whether ComfyUI answers there.
const AskTimeout = 3 * time.Second

// MaxAsking is how many computers are asked at once.
const MaxAsking = 16

// Prober asks candidates whether ComfyUI answers and can render.
type Prober struct {
	// Ping reports whether ComfyUI answers at a URL; nil means asking its
	// system stats, for at most AskTimeout.
	Ping func(ctx context.Context, url string) error
	// Check is the full check of a URL that answered; nil means Check.
	Check func(ctx context.Context, url string, models config.ComfyUI) Status
}

// Probe asks c, unless it is in cfg already or offline on Tailscale: each
// of its URLs in order until one answers, then the check of that one with
// models.
func (p Prober) Probe(ctx context.Context, cfg config.Config, models config.ComfyUI, c Candidate) Found {
	f := Found{}
	if len(c.URLs) > 0 {
		f.URL = c.URLs[0]
	}
	if _, added := c.AddedAs(cfg); added || !c.Online {
		return f
	}
	f.Asked = true
	f.URL, f.Answered, f.Reason = p.Reach(ctx, c)
	if f.Answered {
		check := p.Check
		if check == nil {
			check = Check
		}
		f.Status = check(ctx, f.URL, models)
	}
	return f
}

// Reach asks each of c's URLs in order whether ComfyUI answers there. It
// returns the first that answers or, when none does, c's first URL and why
// the last one did not.
func (p Prober) Reach(ctx context.Context, c Candidate) (url string, answered bool, reason string) {
	ping := p.Ping
	if ping == nil {
		ping = func(ctx context.Context, url string) error {
			ctx, cancel := context.WithTimeout(ctx, AskTimeout)
			defer cancel()
			return Ping(ctx, url)
		}
	}
	reason = "no address to ask"
	for _, u := range c.URLs {
		err := ping(ctx, u)
		// A server that answered with an error still answered; its check
		// says what it answered.
		if err == nil || !errors.Is(err, comfy.ErrUnreachable) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return u, true, ""
		}
		reason = shortReason(err, AskTimeout)
	}
	if len(c.URLs) > 0 {
		url = c.URLs[0]
	}
	return url, false, reason
}

// ProbeAll probes every candidate, at most MaxAsking at once, and calls
// found with each one's index as its result arrives, never two at once. It
// returns once every result is in, or once ctx is done.
func (p Prober) ProbeAll(ctx context.Context, cfg config.Config, models config.ComfyUI, cs []Candidate, found func(i int, f Found)) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, MaxAsking)
	for i, c := range cs {
		if ctx.Err() != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-slots }()
			f := p.Probe(ctx, cfg, models, c)
			if ctx.Err() != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			found(i, f)
		})
	}
	wg.Wait()
}

// NotAnswering says what to do about a computer that is on the tailnet but
// whose ComfyUI does not answer: have it listen on its tailnet address.
func (c Candidate) NotAnswering() string {
	var ports []string
	for _, raw := range c.URLs {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		p := u.Port()
		if p == "" {
			p = "80"
		}
		ports = append(ports, p)
	}
	where := "port " + strings.Join(ports, " or ")
	if len(ports) == 0 {
		where = "its ComfyUI port"
	}
	return fmt.Sprintf("Nothing answers at %s on %s. On that computer, set comfyui.host to %s in its songsmith config and run songsmith server start.",
		c.Address, where, c.Address)
}

// shortReason is, in a few words, why a request failed, such as
// "connection refused".
func shortReason(err error, timeout time.Duration) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("no answer within %s", timeout)
	}
	reason := err.Error()
	if i := strings.LastIndex(reason, ": "); i >= 0 {
		reason = reason[i+2:]
	}
	return reason
}
