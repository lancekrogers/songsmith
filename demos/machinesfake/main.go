// Command machinesfake stands in for the ComfyUI servers of several
// computers while demos/machines.tape records, so the recording reaches no
// real machine: this computer and Studio Mac render slowly and never finish,
// MacBook Air cannot see its VAE file, Den iMac is ready to be added, and
// nothing listens at Attic PC's address. With -seed it instead fills a
// throwaway library and state folder with the songs and measured speeds the
// recording starts from. It is for demos only and listens on loopback.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy/comfytest"
	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/machines"
	"github.com/lancekrogers/songsmith/internal/writer"
)

func main() {
	seed := flag.String("seed", "", "fill the library and state folder of this config, then exit")
	local := flag.String("local", "127.0.0.1:18512", "this computer's ComfyUI")
	studio := flag.String("studio", "127.0.0.1:18513", "Studio Mac's ComfyUI")
	air := flag.String("air", "127.0.0.1:18515", "MacBook Air's ComfyUI, missing its VAE file")
	den := flag.String("den", "127.0.0.1:18516", "Den iMac's ComfyUI, added during the recording")
	flag.Parse()
	if *seed != "" {
		if err := fill(*seed); err != nil {
			log.Fatal(err)
		}
		return
	}
	for _, addr := range []string{*local, *studio, *air, *den} {
		if !strings.HasPrefix(addr, "127.0.0.1:") {
			log.Fatalf("%s: machinesfake listens on loopback only", addr)
		}
	}
	s, err := standIns(*local, *studio, *air, *den)
	if err != nil {
		log.Fatal(err)
	}
	go tick(s.here, 48, 5*time.Second)
	go tick(s.studio, 12, 1500*time.Millisecond)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}

type servers struct{ here, studio, air, den *comfytest.Server }

func (s servers) all() []*comfytest.Server {
	return []*comfytest.Server{s.here, s.studio, s.air, s.den}
}

func (s servers) close() {
	for _, srv := range s.all() {
		if srv != nil {
			srv.Close()
		}
	}
}

// standIns starts the stand-in servers, each holding its prompts and listing
// the model files that computer has.
func standIns(local, studio, air, den string) (servers, error) {
	models := config.Default().ComfyUI
	all := []string{models.DiffusionModel, models.TextEncoder, models.VAE}
	var s servers
	for _, sp := range []struct {
		srv    **comfytest.Server
		addr   string
		name   string
		memory int64
		files  []string
	}{
		{&s.here, local, "local", 64, all},
		{&s.studio, studio, "studio", 128, all},
		{&s.air, air, "air", 16, all[:2]},
		{&s.den, den, "den", 96, all},
	} {
		srv, err := comfytest.Listen(sp.addr, sp.name)
		if err != nil {
			s.close()
			return servers{}, err
		}
		srv.SetHold(true)
		srv.SetMemory(sp.memory * comfytest.GiB)
		srv.SetModels(sp.files...)
		*sp.srv = srv
	}
	return s, nil
}

// tick moves each held prompt on s one sampler step of 100 every interval,
// from first, and never finishes it.
func tick(s *comfytest.Server, first int, interval time.Duration) {
	steps := map[string]int{}
	for range time.Tick(interval) {
		for _, id := range s.Prompts() {
			if !s.Running(id) {
				continue
			}
			if _, ok := steps[id]; !ok {
				steps[id] = first
			}
			steps[id] = min(steps[id]+1, 99)
			s.Progress(id, steps[id], 100)
		}
	}
}

type fixture struct {
	title, idea, lyrics string
	caption             writer.Caption
	status              library.Status
	// machine is where a finished song's take was rendered; jobOn and lost
	// make a song whose render is on a machine that stopped answering.
	machine string
	jobOn   string
	lost    time.Duration
	age     time.Duration
}

var fixtures = []fixture{
	{title: "Paper Moon", status: library.StatusDone, machine: "Studio Mac", age: 26 * time.Hour,
		idea: "a lullaby for a night shift nurse", lyrics: "[Verse]\nPaper moon above the parking lot\nCoffee gone cold\n[Chorus]\nSleep when the sun comes up",
		caption: writer.Caption{GlobalMetadata: "Soft indie folk, 72 BPM, D major.", VocalDetails: "Hushed female lead.", Arrangement: "Verse: fingerpicked guitar. Chorus: warm pads."}},
	{title: "Coast Road", status: library.StatusDone, age: 5 * time.Hour,
		idea: "driving the coast road with the windows down", lyrics: "[Verse]\nSalt on the windshield\nRadio low\n[Chorus]\nCoast road, take me home",
		caption: writer.Caption{GlobalMetadata: "Breezy soft rock, 104 BPM, G major.", VocalDetails: "Warm male lead.", Arrangement: "Verse: clean electric guitar. Chorus: full band."}},
	{title: "Night Drive", status: library.StatusQueued, jobOn: "Attic PC", lost: 3 * time.Minute, age: 40 * time.Minute,
		idea: "an empty highway at 3am", lyrics: "[Verse]\nTail lights fading\nNobody else awake\n[Chorus]\nNight drive, night drive",
		caption: writer.Caption{GlobalMetadata: "Dark synthwave, 96 BPM, E minor.", VocalDetails: "Breathy male lead.", Arrangement: "Verse: arpeggiated synths. Chorus: big gated drums."}},
	{title: "Neon Water", status: library.StatusQueued, age: 20 * time.Minute,
		idea: "synth pop about a city at night", lyrics: "[Verse]\nNeon on the water\nCity in my chest\n[Chorus]\nWe light it up tonight",
		caption: writer.Caption{GlobalMetadata: "Upbeat synth pop, 118 BPM, A minor.", VocalDetails: "Confident female lead.", Arrangement: "Verse: analog bass. Chorus: shimmering pads."}},
	{title: "Lantern Road", status: library.StatusQueued, age: 18 * time.Minute,
		idea: "a slow folk song about walking home by lantern light", lyrics: "[Verse]\nLantern swinging, gravel road\nEvery window gold\n[Chorus]\nCarry me home",
		caption: writer.Caption{GlobalMetadata: "Slow folk, 76 BPM, C major.", VocalDetails: "Weathered male lead.", Arrangement: "Verse: acoustic guitar. Chorus: fiddle joins."}},
	{title: "Glass Harbor", status: library.StatusDraft, age: 10 * time.Minute,
		idea: "boats coming home through fog", lyrics: "[Verse]\nFoghorn calling\nGlass on the bay\n[Chorus]\nBring the boats in",
		caption: writer.Caption{GlobalMetadata: "Cinematic folk, 84 BPM, F major.", VocalDetails: "Choir of mixed voices.", Arrangement: "Verse: piano. Chorus: strings swell."}},
}

// fill writes the fixture songs and the measured speeds under the folders
// the config at path names, refusing any folder outside /tmp.
func fill(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	for _, dir := range []string{cfg.LibraryDir, cfg.StateDir} {
		if !strings.HasPrefix(dir, "/tmp/songsmith-demo-") {
			return fmt.Errorf("%s is not a throwaway demo folder under /tmp/songsmith-demo-", dir)
		}
	}
	store := library.NewStore(cfg.LibraryDir)
	now := time.Now()
	for i, f := range fixtures {
		s, err := store.Create(library.Song{Title: f.title, Idea: f.idea, Lyrics: f.lyrics, Caption: f.caption, Seconds: 120, Seed: int64(4100 + i)})
		if err != nil {
			return err
		}
		s.Created, s.Status = now.Add(-f.age), f.status
		switch {
		case f.status == library.StatusDone:
			s.Audio, s.Duration = "song.flac", 118
			s.Started, s.Finished = now.Add(-f.age), now.Add(-f.age+14*time.Minute)
			s.Takes = []library.Take{{File: s.Audio, Kind: library.TakeRender, Machine: f.machine, Created: s.Finished, Duration: s.Duration}}
			if err := os.WriteFile(store.AudioPath(s), nil, 0o644); err != nil {
				return err
			}
		case f.jobOn != "":
			s.JobID, s.RenderingOn, s.LostSince = "attic-7", f.jobOn, now.Add(-f.lost)
		}
		if err := store.Save(s); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	speeds := machines.NewSpeeds(cfg.StateDir)
	for range 3 {
		if err := speeds.Record("Studio Mac", 36*time.Minute, 120); err != nil {
			return err
		}
	}
	return speeds.Record(config.ThisComputer, 82*time.Minute, 120)
}
