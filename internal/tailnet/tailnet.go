// Package tailnet reads what the Tailscale on this computer knows about the
// other computers on its tailnet. It only reads: it never changes Tailscale.
package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Node is another computer on the tailnet.
type Node struct {
	// Name is what to call it: its host name, or the first part of its DNS
	// name when it reports none.
	Name string
	// DNSName is its full MagicDNS name, such as studio.example-tailnet.ts.net;
	// Short is its first part, such as studio.
	DNSName string
	Short   string
	IPs     []string
	// Address is the one to reach it at: its first IPv4, else its first IPv6.
	Address string
	Online  bool
	OS      string
}

// ErrNotInstalled is wrapped in the error returned when this computer has
// no Tailscale to ask.
var ErrNotInstalled = errors.New("Tailscale is not installed on this computer")

// AppCLI is where the Mac app keeps its command line, which it does not put
// on PATH.
const AppCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// Timeout bounds reading the status. Tailscale answers from what it already
// knows, so a slow answer means it is stuck.
const Timeout = 5 * time.Second

// Reader asks the Tailscale on this computer. Its zero value runs the real
// command; tests set the functions.
type Reader struct {
	LookPath func(file string) (string, error)
	Exists   func(path string) bool
	// Run runs cli status --json and returns what it printed.
	Run func(ctx context.Context, cli string) (stdout, stderr []byte, err error)
}

// Peers returns the other computers on this computer's tailnet.
func Peers(ctx context.Context) ([]Node, error) { return Reader{}.Peers(ctx) }

// Peers returns the other computers on this computer's tailnet.
func (r Reader) Peers(ctx context.Context) ([]Node, error) {
	cli, err := r.cli()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	run := r.Run
	if run == nil {
		run = runStatus
	}
	out, stderr, err := run(ctx, cli)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Tailscale did not answer within %s; check that it runs on this computer and try again", Timeout)
		}
		detail := strings.TrimSpace(string(stderr))
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("asking Tailscale for the other computers failed (%s); check that Tailscale runs on this computer and try again", detail)
	}
	return Parse(out)
}

// cli finds the tailscale command: on PATH, else inside the Mac app.
func (r Reader) cli() (string, error) {
	look, exists := r.LookPath, r.Exists
	if look == nil {
		look = exec.LookPath
	}
	if exists == nil {
		exists = func(p string) bool {
			info, err := os.Stat(p)
			return err == nil && !info.IsDir()
		}
	}
	if p, err := look("tailscale"); err == nil {
		return p, nil
	}
	if exists(AppCLI) {
		return AppCLI, nil
	}
	return "", fmt.Errorf("%w; install it from tailscale.com, or add a machine by its address", ErrNotInstalled)
}

func runStatus(ctx context.Context, cli string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, cli, "status", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type status struct {
	BackendState   string          `json:"BackendState"`
	MagicDNSSuffix string          `json:"MagicDNSSuffix"`
	Self           *node           `json:"Self"`
	Peer           map[string]node `json:"Peer"`
}

type node struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	OS           string   `json:"OS"`
	ShareeNode   bool     `json:"ShareeNode"`
	Tags         []string `json:"Tags"`
}

// Parse reads the output of tailscale status --json into the other
// computers that could run ComfyUI, sorted by name. Left out are this
// computer, phones and tablets, computers shared in from another tailnet,
// Mullvad exit nodes and any computer with no address.
func Parse(data []byte) ([]Node, error) {
	unreadable := errors.New("songsmith could not read what Tailscale said about the other computers; update Tailscale and try again")
	start := bytes.IndexByte(data, '{')
	if start < 0 {
		return nil, unreadable
	}
	var st status
	if err := json.Unmarshal(data[start:], &st); err != nil {
		return nil, unreadable
	}
	if st.BackendState != "" && st.BackendState != "Running" {
		return nil, errors.New(backendState(st.BackendState))
	}
	suffix := strings.ToLower(trimDot(st.MagicDNSSuffix))
	var self node
	if st.Self != nil {
		self = *st.Self
	}
	var out []Node
	for _, p := range st.Peer {
		n, ok := p.usable(suffix)
		if !ok || isSelf(n, self) {
			continue
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b Node) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Address, b.Address)
	})
	return out, nil
}

// backendState says what to do about a Tailscale that is not running.
func backendState(state string) string {
	switch state {
	case "NeedsLogin":
		return "Tailscale is signed out on this computer; sign in (tailscale up) and try again"
	case "Stopped":
		return "Tailscale is stopped on this computer; start it and try again"
	case "NoState", "Starting":
		return "Tailscale is not ready yet on this computer; wait for it to connect and try again"
	}
	return fmt.Sprintf("Tailscale is not running on this computer (it says %s); start it and try again", state)
}

func (p node) usable(suffix string) (Node, bool) {
	dns := trimDot(p.DNSName)
	switch sys := strings.ToLower(strings.TrimSpace(p.OS)); {
	case p.ShareeNode, sys == "ios", sys == "android", sys == "tvos", slices.Contains(p.Tags, "tag:mullvad-exit-node"):
		return Node{}, false
	case suffix != "" && !strings.HasSuffix(strings.ToLower(dns), "."+suffix):
		return Node{}, false
	}
	n := Node{DNSName: dns, Online: p.Online, OS: strings.TrimSpace(p.OS)}
	n.Short, _, _ = strings.Cut(dns, ".")
	var v6 string
	for _, raw := range p.TailscaleIPs {
		ip, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		n.IPs = append(n.IPs, ip.String())
		switch {
		case ip.Is4() && n.Address == "":
			n.Address = ip.String()
		case !ip.Is4() && v6 == "":
			v6 = ip.String()
		}
	}
	if n.Address == "" {
		n.Address = v6
	}
	if n.Address == "" {
		return Node{}, false
	}
	n.Name = strings.TrimSpace(p.HostName)
	if n.Name == "" || strings.EqualFold(n.Name, "localhost") {
		n.Name = n.Short
	}
	if n.Name == "" {
		n.Name = n.Address
	}
	return n, true
}

// isSelf reports whether n is this computer, by its DNS name or an address.
func isSelf(n Node, self node) bool {
	if d := trimDot(self.DNSName); d != "" && strings.EqualFold(d, n.DNSName) {
		return true
	}
	for _, raw := range self.TailscaleIPs {
		if ip, err := netip.ParseAddr(strings.TrimSpace(raw)); err == nil && slices.Contains(n.IPs, ip.String()) {
			return true
		}
	}
	return false
}

func trimDot(s string) string { return strings.TrimSuffix(strings.TrimSpace(s), ".") }
