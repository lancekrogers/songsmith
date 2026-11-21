package tailnet

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const sample = `{
  "BackendState": "Running",
  "MagicDNSSuffix": "example-tailnet.ts.net",
  "Self": {"HostName": "Laptop", "DNSName": "laptop.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.1", "fd7a:115c:a1e0::1"], "Online": true, "OS": "macOS"},
  "Peer": {
    "k1": {"HostName": "Studio Mac", "DNSName": "studio.example-tailnet.ts.net.", "TailscaleIPs": ["fd7a:115c:a1e0::2", "100.64.0.2"], "Online": true, "OS": "macOS"},
    "k2": {"HostName": "attic-pc", "DNSName": "attic-pc.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.3"], "Online": false, "OS": "windows"},
    "k3": {"HostName": "localhost", "DNSName": "den-imac.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.4"], "Online": true, "OS": "linux"},
    "k4": {"HostName": "v6box", "DNSName": "v6box.example-tailnet.ts.net.", "TailscaleIPs": ["fd7a:115c:a1e0::5"], "Online": true, "OS": "linux"},
    "k5": {"HostName": "Phone", "DNSName": "phone.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.6"], "Online": true, "OS": "iOS"},
    "k6": {"HostName": "Tablet", "DNSName": "tablet.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.7"], "Online": true, "OS": "android"},
    "k7": {"HostName": "TV", "DNSName": "tv.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.8"], "Online": true, "OS": "tvOS"},
    "k8": {"HostName": "friend", "DNSName": "friend.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.9"], "Online": true, "OS": "linux", "ShareeNode": true},
    "k9": {"HostName": "shared-in", "DNSName": "shared-in.other-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.10"], "Online": true, "OS": "linux"},
    "k10": {"HostName": "exit-relay-1", "DNSName": "exit-relay-1.mullvad.ts.net.", "TailscaleIPs": ["100.64.0.11"], "Online": true, "OS": "linux"},
    "k11": {"HostName": "exit", "DNSName": "exit.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.12"], "Online": true, "OS": "linux", "Tags": ["tag:mullvad-exit-node"]},
    "k12": {"HostName": "noaddr", "DNSName": "noaddr.example-tailnet.ts.net.", "TailscaleIPs": [], "Online": true, "OS": "linux"},
    "k13": {"HostName": "Laptop", "DNSName": "laptop.example-tailnet.ts.net.", "TailscaleIPs": ["100.64.0.1"], "Online": true, "OS": "macOS"}
  }
}`

func names(nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

func TestParseKeepsOnlyOtherComputersThatCouldRunComfyUI(t *testing.T) {
	nodes, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(nodes), []string{"attic-pc", "den-imac", "Studio Mac", "v6box"}; !slices.Equal(got, want) {
		t.Fatalf("names %q, want %q", got, want)
	}
	studio := nodes[2]
	if studio.DNSName != "studio.example-tailnet.ts.net" || studio.Short != "studio" || studio.Address != "100.64.0.2" ||
		!slices.Equal(studio.IPs, []string{"fd7a:115c:a1e0::2", "100.64.0.2"}) || !studio.Online || studio.OS != "macOS" {
		t.Fatalf("studio %+v", studio)
	}
	if nodes[0].Online {
		t.Errorf("attic-pc is offline: %+v", nodes[0])
	}
	if nodes[1].Name != "den-imac" {
		t.Errorf("a host name of localhost should fall back to the DNS label: %+v", nodes[1])
	}
	if nodes[3].Address != "fd7a:115c:a1e0::5" {
		t.Errorf("an IPv6-only computer is reached at its IPv6: %+v", nodes[3])
	}
}

func TestParseSkipsABannerBeforeTheJSON(t *testing.T) {
	nodes, err := Parse([]byte("Warning: client version differs from the server\n" + sample))
	if err != nil || len(nodes) != 4 {
		t.Fatalf("%v %+v", err, nodes)
	}
}

func TestParseWithoutASuffixKeepsEveryDNSName(t *testing.T) {
	data := `{"BackendState":"Running","Peer":{
	  "a":{"HostName":"","DNSName":"box.other.ts.net.","TailscaleIPs":["100.64.0.20"],"Online":true},
	  "b":{"HostName":"Alpha","DNSName":"","TailscaleIPs":["100.64.0.22"],"Online":true},
	  "c":{"HostName":"alpha","DNSName":"","TailscaleIPs":["100.64.0.21"],"Online":true}}}`
	nodes, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(nodes), []string{"alpha", "Alpha", "box"}; !slices.Equal(got, want) {
		t.Fatalf("names %q, want %q", got, want)
	}
	if nodes[0].Address != "100.64.0.21" || nodes[1].Address != "100.64.0.22" {
		t.Fatalf("a tie on name sorts by address: %+v", nodes)
	}
}

func TestParseSaysWhatToDoWhenTailscaleIsNotRunning(t *testing.T) {
	for state, want := range map[string]string{
		"NeedsLogin": "Tailscale is signed out on this computer; sign in (tailscale up) and try again",
		"Stopped":    "Tailscale is stopped on this computer; start it and try again",
		"NoState":    "Tailscale is not ready yet on this computer; wait for it to connect and try again",
		"Starting":   "Tailscale is not ready yet on this computer; wait for it to connect and try again",
		"Weird":      "Tailscale is not running on this computer (it says Weird); start it and try again",
	} {
		_, err := Parse([]byte(`{"BackendState":"` + state + `","Peer":{}}`))
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v", state, err)
		}
	}
}

func TestParseSaysWhenItCannotReadTheOutput(t *testing.T) {
	for _, data := range []string{"", "command not found", `{"Peer":{`} {
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "could not read what Tailscale said") {
			t.Errorf("%q: %v", data, err)
		}
	}
}

func notFound(string) (string, error) { return "", exec.ErrNotFound }

func TestTheCommandIsFoundOnPathThenInTheMacApp(t *testing.T) {
	var ran string
	run := func(_ context.Context, cli string) ([]byte, []byte, error) {
		ran = cli
		return []byte(sample), nil, nil
	}
	r := Reader{LookPath: func(string) (string, error) { return "/usr/local/bin/tailscale", nil }, Exists: func(string) bool { return true }, Run: run}
	if _, err := r.Peers(t.Context()); err != nil || ran != "/usr/local/bin/tailscale" {
		t.Fatalf("on PATH: ran %q, %v", ran, err)
	}
	r.LookPath = notFound
	r.Exists = func(p string) bool { return p == AppCLI }
	if nodes, err := r.Peers(t.Context()); err != nil || ran != AppCLI || len(nodes) != 4 {
		t.Fatalf("in the app: ran %q, %v", ran, err)
	}
}

func TestNoTailscaleIsToldApart(t *testing.T) {
	r := Reader{LookPath: notFound, Exists: func(string) bool { return false },
		Run: func(context.Context, string) ([]byte, []byte, error) {
			t.Fatal("ran a command with no Tailscale installed")
			return nil, nil, nil
		}}
	_, err := r.Peers(t.Context())
	if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "add a machine by its address") {
		t.Fatalf("%v", err)
	}
}

func TestAFailedCommandSaysWhatItPrinted(t *testing.T) {
	r := Reader{LookPath: func(string) (string, error) { return "tailscale", nil },
		Run: func(context.Context, string) ([]byte, []byte, error) {
			return nil, []byte("failed to connect to local tailscaled\n"), errors.New("exit status 1")
		}}
	_, err := r.Peers(t.Context())
	if err == nil || errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "(failed to connect to local tailscaled)") {
		t.Fatalf("%v", err)
	}
}
