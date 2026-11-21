package machines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/tailnet"
)

func TestCandidatesAreAskedOnTheDefaultPortThenThisComputersPort(t *testing.T) {
	nodes := []tailnet.Node{
		{Name: "Studio Mac", DNSName: "studio.example-tailnet.ts.net", Short: "studio", IPs: []string{"100.64.0.2", "fd7a:115c:a1e0::2"}, Address: "100.64.0.2", Online: true, OS: "macOS"},
		{Name: "v6box", DNSName: "v6box.example-tailnet.ts.net", Short: "v6box", IPs: []string{"fd7a:115c:a1e0::5"}, Address: "fd7a:115c:a1e0::5", Online: true},
	}
	cs := candidates(nodes, 18512)
	if got := cs[0].URLs; !slices.Equal(got, []string{"http://100.64.0.2:8188", "http://100.64.0.2:18512"}) {
		t.Fatalf("urls %q", got)
	}
	if got := cs[0].Hosts; !slices.Equal(got, []string{"studio.example-tailnet.ts.net", "studio", "100.64.0.2", "fd7a:115c:a1e0::2"}) {
		t.Fatalf("hosts %q", got)
	}
	if got := cs[1].URLs; !slices.Equal(got, []string{"http://[fd7a:115c:a1e0::5]:8188", "http://[fd7a:115c:a1e0::5]:18512"}) {
		t.Fatalf("an IPv6 address is bracketed: %q", got)
	}
	if got := candidates(nodes, DefaultPort)[0].URLs; len(got) != 1 {
		t.Fatalf("the default port is asked once: %q", got)
	}
}

func studioCandidate(urls ...string) Candidate {
	return Candidate{Name: "Studio Mac", Online: true, OS: "macOS", Address: "100.64.0.2", URLs: urls,
		Hosts: []string{"studio.example-tailnet.ts.net", "studio", "100.64.0.2", "fd7a:115c:a1e0::2"}}
}

func TestACandidateIsAlreadyAddedUnderAnyOfItsNames(t *testing.T) {
	c := studioCandidate("http://100.64.0.2:8188")
	for _, saved := range []string{
		"http://100.64.0.2:8188",
		"http://Studio.Example-Tailnet.ts.net:8188",
		"http://studio.example-tailnet.ts.net.:9000",
		"http://studio:8188",
		"http://[fd7a:115c:a1e0:0::2]:8188",
		"http://[::ffff:100.64.0.2]:8188",
	} {
		cfg := config.Config{Machines: []config.Machine{{Name: "Big Mac", URL: saved}}}
		if name, ok := c.AddedAs(cfg); !ok || name != "Big Mac" {
			t.Errorf("saved at %s: %q %v", saved, name, ok)
		}
	}
	for _, other := range []string{"http://100.64.0.3:8188", "http://studio-2.example-tailnet.ts.net:8188", "http://127.0.0.1:8188"} {
		cfg := config.Config{Machines: []config.Machine{{Name: "Other", URL: other}}}
		if name, ok := c.AddedAs(cfg); ok {
			t.Errorf("%s matched as %q", other, name)
		}
	}
	loopback := Candidate{Name: "Den iMac", Online: true, Address: "100.64.0.4", URLs: []string{"http://127.0.0.1:18516"}, Hosts: []string{"100.64.0.4"}}
	cfg := config.Config{Machines: []config.Machine{{Name: "Attic", URL: "http://127.0.0.1:18514"}, {Name: "Den", URL: "http://127.0.0.1:18516"}}}
	if name, ok := loopback.AddedAs(cfg); !ok || name != "Den" {
		t.Errorf("exact URL: %q %v", name, ok)
	}
}

// recorder wraps the real ping and check, keeping each URL asked.
type recorder struct {
	mu    sync.Mutex
	asked []string
}

func (r *recorder) prober() Prober {
	return Prober{
		Ping: func(ctx context.Context, url string) error {
			r.mu.Lock()
			r.asked = append(r.asked, url)
			r.mu.Unlock()
			ctx, cancel := context.WithTimeout(ctx, AskTimeout)
			defer cancel()
			return Ping(ctx, url)
		},
	}
}

func (r *recorder) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

func TestProbeAllSaysWhatEachComputerHas(t *testing.T) {
	models := config.Default().ComfyUI
	ready := comfytest.New(t, "den")
	ready.SetMemory(96 * comfytest.GiB)
	ready.SetModels(models.DiffusionModel, models.TextEncoder, models.VAE)
	missing := comfytest.New(t, "air")
	missing.SetModels(models.DiffusionModel, models.TextEncoder)
	closed := comfytest.New(t, "kitchen")
	closed.Close()
	var offlineHits atomic.Int32
	offline := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { offlineHits.Add(1) }))
	t.Cleanup(offline.Close)
	var addedHits atomic.Int32
	added := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { addedHits.Add(1) }))
	t.Cleanup(added.Close)

	cs := []Candidate{
		{Name: "Den iMac", Online: true, Address: "100.64.0.4", URLs: []string{ready.URL}, Hosts: []string{"100.64.0.4"}},
		{Name: "MacBook Air", Online: true, Address: "100.64.0.5", URLs: []string{missing.URL}, Hosts: []string{"100.64.0.5"}},
		{Name: "Kitchen PC", Online: true, Address: "100.64.0.6", URLs: []string{closed.URL}, Hosts: []string{"100.64.0.6"}},
		{Name: "Garage Mini", Online: false, Address: "100.64.0.7", URLs: []string{offline.URL}, Hosts: []string{"100.64.0.7"}},
		{Name: "Studio Mac", Online: true, Address: "100.64.0.2", URLs: []string{added.URL}, Hosts: []string{"studio.example-tailnet.ts.net", "100.64.0.2"}},
		{Name: "Studio by URL", Online: true, Address: "100.64.0.8", URLs: []string{added.URL}, Hosts: []string{"100.64.0.8"}},
	}
	cfg := config.Default()
	cfg.Machines = []config.Machine{{Name: "Big Mac", URL: "http://studio.example-tailnet.ts.net:8188"}, {Name: "Same URL", URL: "http://127.0.0.1:1"}}
	cs[5].URLs = []string{"http://127.0.0.1:1"}

	rec := &recorder{}
	got := map[int]Found{}
	rec.prober().ProbeAll(t.Context(), cfg, models, cs, func(i int, f Found) {
		if _, dup := got[i]; dup {
			t.Errorf("%d delivered twice", i)
		}
		got[i] = f
	})
	if len(got) != len(cs) {
		t.Fatalf("got %d results for %d computers", len(got), len(cs))
	}
	if f := got[0]; !f.Asked || !f.Answered || f.URL != ready.URL || f.Status.State != Ready || f.Status.Memory != 96*comfytest.GiB {
		t.Errorf("ready: %+v", f)
	}
	if f := got[1]; !f.Answered || f.Status.State != ModelsMissing || !slices.Contains(f.Status.Missing, models.VAE) {
		t.Errorf("models missing: %+v", f)
	}
	if f := got[2]; !f.Asked || f.Answered || f.URL != closed.URL || f.Reason != "connection refused" {
		t.Errorf("not answering: %+v", f)
	}
	if f := got[3]; f.Asked || f.Answered || f.URL != offline.URL {
		t.Errorf("offline on Tailscale: %+v", f)
	}
	if f := got[4]; f.Asked {
		t.Errorf("added under its DNS name: %+v", f)
	}
	if f := got[5]; f.Asked {
		t.Errorf("added at its URL: %+v", f)
	}
	if offlineHits.Load() != 0 || addedHits.Load() != 0 {
		t.Errorf("asked a computer it should leave alone: offline %d, added %d", offlineHits.Load(), addedHits.Load())
	}
	for _, u := range rec.urls() {
		if u == offline.URL || u == added.URL || u == "http://127.0.0.1:1" {
			t.Errorf("pinged %s", u)
		}
	}
}

func TestTheSecondURLIsUsedWhenTheFirstDoesNotAnswer(t *testing.T) {
	closed := comfytest.New(t, "first")
	closed.Close()
	srv := comfytest.New(t, "second")
	c := Candidate{Name: "Den iMac", Online: true, Address: "100.64.0.4", URLs: []string{closed.URL, srv.URL}}
	rec := &recorder{}
	f := rec.prober().Probe(t.Context(), config.Default(), config.Default().ComfyUI, c)
	if !f.Answered || f.URL != srv.URL {
		t.Fatalf("%+v", f)
	}
	if got := rec.urls(); !slices.Equal(got, []string{closed.URL, srv.URL}) {
		t.Fatalf("asked %q", got)
	}

	srv.Close()
	f = rec.prober().Probe(t.Context(), config.Default(), config.Default().ComfyUI, c)
	if f.Answered || f.URL != closed.URL {
		t.Fatalf("neither answers, so it is saved with the first: %+v", f)
	}
}

func TestProbeAllAsksAtMostSixteenAtOnce(t *testing.T) {
	var now, most atomic.Int32
	p := Prober{
		Ping: func(context.Context, string) error {
			n := now.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			now.Add(-1)
			return nil
		},
		Check: func(context.Context, string, config.ComfyUI) Status { return Status{State: Ready} },
	}
	var cs []Candidate
	for i := range 50 {
		cs = append(cs, Candidate{Name: fmt.Sprint(i), Online: true, Address: fmt.Sprintf("100.64.1.%d", i), URLs: []string{fmt.Sprintf("http://100.64.1.%d:8188", i)}})
	}
	n := 0
	p.ProbeAll(t.Context(), config.Default(), config.Default().ComfyUI, cs, func(int, Found) { n++ })
	if n != len(cs) {
		t.Fatalf("%d results", n)
	}
	if m := most.Load(); m > MaxAsking || m < 2 {
		t.Fatalf("%d asked at once", m)
	}
}

func TestProbeAllStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{}, 100)
	p := Prober{Ping: func(ctx context.Context, _ string) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	var cs []Candidate
	for i := range 40 {
		cs = append(cs, Candidate{Name: fmt.Sprint(i), Online: true, URLs: []string{fmt.Sprintf("http://100.64.1.%d:8188", i)}})
	}
	go func() {
		<-started
		cancel()
	}()
	done := make(chan int)
	go func() {
		n := 0
		p.ProbeAll(ctx, config.Default(), config.Default().ComfyUI, cs, func(int, Found) { n++ })
		done <- n
	}()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("%d results after cancelling", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProbeAll kept going after its context was cancelled")
	}
	if len(started) >= len(cs) {
		t.Fatalf("asked all %d after cancelling", len(started))
	}
}

func TestNotAnsweringNamesTheAddressAndThePorts(t *testing.T) {
	c := Candidate{Address: "100.64.0.7", URLs: []string{"http://100.64.0.7:8188"}}
	want := "Nothing answers at 100.64.0.7 on port 8188. On that computer, set comfyui.host to 100.64.0.7 in its songsmith config and run songsmith server start."
	if got := c.NotAnswering(); got != want {
		t.Fatalf("%q", got)
	}
	c.URLs = append(c.URLs, "http://100.64.0.7:18512")
	if got := c.NotAnswering(); !strings.HasPrefix(got, "Nothing answers at 100.64.0.7 on port 8188 or 18512. ") {
		t.Fatalf("%q", got)
	}
}
