package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/lancekrogers/songsmith/internal/comfy"
	comfyserver "github.com/lancekrogers/songsmith/internal/comfy/server"
	"github.com/lancekrogers/songsmith/internal/config"
)

// libraryModel is one made-up model of the stand-in Ollama library.
type libraryModel struct {
	name, family, size, quant string
	caps                      []string
}

// libraryModels are about thirty made-up models: two embedding models, one
// that only /api/show describes, and the writer model the demo config names.
var libraryModels = func() []libraryModel {
	out := []libraryModel{
		{name: "songwriter:7b", family: "llama", size: "7.2B", quant: "Q4_K_M", caps: []string{"completion"}},
		{name: "wordvec-embed:latest", family: "nomic-bert", size: "137M", quant: "F16"},
		{name: "minivec:33m", family: "bert", size: "33M", quant: "F16", caps: []string{"embedding"}},
		{name: "oldtimer:13b", family: "llama", size: "13B", quant: "Q4_0"},
	}
	names := []string{"ballad", "bard", "cantor", "chorus", "ditty", "fable", "hymnal", "lyricist", "madrigal", "minstrel",
		"muse", "ode", "poet", "refrain", "rhyme", "serenade", "sonnet", "storyteller", "troubadour", "verse", "villanelle", "wordsmith", "anthem", "lullaby", "shanty", "elegy"}
	sizes := []string{"3B", "8B", "14B", "4B", "12B", "27B", "9B", "32B", "1.5B", "70B"}
	quants := []string{"Q4_K_M", "Q8_0", "Q5_K_M", "Q4_K_S"}
	for i, n := range names {
		out = append(out, libraryModel{name: fmt.Sprintf("%s:%s", n, "latest"), family: "qwen3", size: sizes[i%len(sizes)],
			quant: quants[i%len(quants)], caps: []string{"completion", "tools"}})
	}
	return out
}()

// cannedDraft is the draft the stand-in Ollama writes for any idea.
var cannedDraft = map[string]any{
	"title": "Last Light",
	"lyrics": "[Verse]\nThe lamp still turns for no one\nSalt on every stair\nI log the ships that never come\nAnd wind the clock with care\n\n" +
		"[Chorus]\nKeep the light on, keep the light on\nOne more night upon the rock\nKeep the light on, keep the light on\nTill the dawn undoes the lock\n\n" +
		"[Verse]\nThe radio says they'll take it down\nA switch will do my job\nBut tonight the beam goes round and round\nAnd I can hear the fog\n\n" +
		"[Chorus]\nKeep the light on, keep the light on\nOne more night upon the rock\n\n[Outro]\nKeep the light on",
	"caption": map[string]string{
		"global_metadata": "Folk ballad, 72 BPM, D minor; sparse and warm, a slow build into the last chorus; late-night listening.",
		"vocal_details":   "Weathered male lead, low register, close and unhurried; a soft harmony joins on the final chorus.",
		"arrangement":     "Verse: fingerpicked acoustic guitar and room tone. Chorus: brushed drums and upright bass enter, a fiddle line answers the vocal. Outro: guitar alone, fading.",
	},
	"seconds": 150,
}

// serveLibrary answers like an Ollama with libraryModels installed: tags,
// show for the model tags leave undescribed, and a chat that returns
// cannedDraft after a short pause, or unloads when sent no messages.
func serveLibrary(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		models := make([]map[string]any, 0, len(libraryModels))
		for _, m := range libraryModels {
			entry := map[string]any{"name": m.name, "model": m.name,
				"details": map[string]any{"family": m.family, "families": []string{m.family}, "parameter_size": m.size, "quantization_level": m.quant}}
			if m.caps != nil {
				entry["capabilities"] = m.caps
			}
			models = append(models, entry)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		caps := []string{"completion"}
		if req.Model == "wordvec-embed:latest" {
			caps = []string{"embedding"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string
			Messages []json.RawMessage
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "message": map[string]string{"role": "assistant"}, "done": true, "done_reason": "unload"})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2500 * time.Millisecond):
		}
		content, _ := json.Marshal(cannedDraft)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "message": map[string]string{"role": "assistant", "content": string(content)}, "done": true})
	})
	log.Printf("stand-in Ollama library on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// comfySamplers are the sampler names a ComfyUI of this era lists.
var comfySamplers = []string{"euler", "euler_cfg_pp", "euler_ancestral", "euler_ancestral_cfg_pp", "heun", "heunpp2", "dpm_2", "dpm_2_ancestral",
	"lms", "dpm_fast", "dpm_adaptive", "dpmpp_2s_ancestral", "dpmpp_2s_ancestral_cfg_pp", "dpmpp_sde", "dpmpp_sde_gpu", "dpmpp_2m",
	"dpmpp_2m_cfg_pp", "dpmpp_2m_sde", "dpmpp_2m_sde_gpu", "dpmpp_3m_sde", "dpmpp_3m_sde_gpu", "ddpm", "lcm", "ipndm", "ipndm_v",
	"deis", "res_multistep", "res_multistep_cfg_pp", "gradient_estimation", "er_sde", "seeds_2", "seeds_3", "sa_solver", "ddim", "uni_pc", "uni_pc_bh2"}

var comfySchedulers = []string{"simple", "sgm_uniform", "karras", "exponential", "ddim_uniform", "beta", "normal", "linear_quadratic", "kl_optimal"}

// serveComfy answers like a running ComfyUI started outside songsmith with
// the MiniMax Music 3 nodes and the default model files, so Settings lists
// its samplers. It renders nothing.
func serveComfy(addr string) {
	files := config.Default().ComfyUI
	combos := map[string][]string{"sampler_name": comfySamplers, "scheduler": comfySchedulers,
		"unet_name": {files.DiffusionModel}, "clip_name": {files.TextEncoder}, "vae_name": {files.VAE}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /system_stats", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"system": map[string]any{"argv": []string{"main.py", comfyserver.RequiredFlag}},
			"devices": []map[string]string{{"type": "mps"}}})
	})
	mux.HandleFunc("GET /object_info/{class}", func(w http.ResponseWriter, r *http.Request) {
		class := r.PathValue("class")
		inputs, ok := comfy.RequiredNodes[class]
		if !ok {
			_, _ = w.Write([]byte("{}"))
			return
		}
		required := map[string]any{}
		for _, in := range inputs {
			if opts, ok := combos[in]; ok {
				required[in] = []any{opts}
			} else {
				required[in] = []any{"STRING", map[string]any{}}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{class: map[string]any{"input": map[string]any{"required": required}}})
	})
	log.Printf("stand-in ComfyUI on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
