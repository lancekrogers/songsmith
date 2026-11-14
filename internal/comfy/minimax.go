package comfy

import (
	"cmp"
	"path"
	"strings"
)

// Node IDs in the MiniMax graph. Progress events carry the executing node ID,
// so these double as stage names.
const (
	NodeTextEncoder = "text_encoder"
	NodeDiffusion   = "diffusion_model"
	NodeVAE         = "vae"
	NodeEncode      = "encode"
	NodeNegative    = "negative"
	NodeLatent      = "latent"
	NodeSampler     = "sampler"
	NodeDecode      = "decode"
	NodeSave        = "save"
)

// MiniMaxParams configures one MiniMax Music 3 render. The graph mirrors the
// official ComfyUI "audio_minimax_music_3" template.
type MiniMaxParams struct {
	Caption    string
	Lyrics     string
	Seed       int64
	MaxSeconds float64
	CFGScale   float64
	TopK       int
	Steps      int
	// Sampler and Scheduler default to "euler" and "simple".
	Sampler        string
	Scheduler      string
	DiffusionModel string
	TextEncoder    string
	VAE            string
	FilenamePrefix string
}

func MiniMaxGraph(p MiniMaxParams) Graph {
	return Graph{
		NodeTextEncoder: {ClassType: "CLIPLoader", Inputs: map[string]any{
			"clip_name": p.TextEncoder, "type": "minimax", "device": "default",
		}},
		NodeDiffusion: {ClassType: "UNETLoader", Inputs: map[string]any{
			"unet_name": p.DiffusionModel, "weight_dtype": "default",
		}},
		NodeVAE: {ClassType: "VAELoader", Inputs: map[string]any{
			"vae_name": p.VAE,
		}},
		NodeEncode: {ClassType: "MiniMaxMusic3TextEncode", Inputs: map[string]any{
			"clip":         Link(NodeTextEncoder, 0),
			"caption":      p.Caption,
			"lyrics":       p.Lyrics,
			"seed":         p.Seed,
			"max_duration": p.MaxSeconds,
			"cfg_scale":    p.CFGScale,
			"top_k":        p.TopK,
		}},
		NodeNegative: {ClassType: "ConditioningZeroOut", Inputs: map[string]any{
			"conditioning": Link(NodeEncode, 0),
		}},
		// The encoder decides the song's real length; its second output feeds the latent size.
		NodeLatent: {ClassType: "EmptyMiniMaxMusic3LatentAudio", Inputs: map[string]any{
			"seconds": Link(NodeEncode, 1), "batch_size": 1,
		}},
		NodeSampler: {ClassType: "KSampler", Inputs: map[string]any{
			"model":        Link(NodeDiffusion, 0),
			"seed":         p.Seed,
			"steps":        p.Steps,
			"cfg":          p.CFGScale,
			"sampler_name": cmp.Or(p.Sampler, "euler"),
			"scheduler":    cmp.Or(p.Scheduler, "simple"),
			"positive":     Link(NodeEncode, 0),
			"negative":     Link(NodeNegative, 0),
			"latent_image": Link(NodeLatent, 0),
			"denoise":      1.0,
		}},
		NodeDecode: decodeNode(NodeSampler),
		NodeSave: {ClassType: "SaveAudio", Inputs: map[string]any{
			"audio": Link(NodeDecode, 0), "filename_prefix": p.FilenamePrefix,
		}},
	}
}

// Node IDs only the two-prompt render uses.
const (
	NodeSaveLatent = "save_latent"
	NodeLoadLatent = "load_latent"
)

// MiniMaxSampleGraph is the first prompt of a render split in two to save
// memory: MiniMaxGraph up to the sampler, whose latent it saves under
// latentPrefix in the output folder instead of decoding it.
func MiniMaxSampleGraph(p MiniMaxParams, latentPrefix string) Graph {
	g := MiniMaxGraph(p)
	delete(g, NodeVAE)
	delete(g, NodeDecode)
	delete(g, NodeSave)
	g[NodeSaveLatent] = Node{ClassType: "SaveLatent", Inputs: map[string]any{
		"samples": Link(NodeSampler, 0), "filename_prefix": latentPrefix,
	}}
	return g
}

// MiniMaxDecodeGraph is the second prompt: it decodes the latent the first
// one saved, named as LatentRef names it, with only the VAE loaded.
func MiniMaxDecodeGraph(vae, latent, filenamePrefix string) Graph {
	return Graph{
		NodeVAE: {ClassType: "VAELoader", Inputs: map[string]any{
			"vae_name": vae,
		}},
		NodeLoadLatent: {ClassType: "LoadLatent", Inputs: map[string]any{
			"latent": latent,
		}},
		NodeDecode: decodeNode(NodeLoadLatent),
		NodeSave: {ClassType: "SaveAudio", Inputs: map[string]any{
			"audio": Link(NodeDecode, 0), "filename_prefix": filenamePrefix,
		}},
	}
}

// LatentRef names a saved latent the way LoadLatent reads it: a path under
// the folder its type names, which LoadLatent finds by the "[output]" tag.
func LatentRef(f OutputFile) string {
	return path.Join(f.Subfolder, f.Filename) + " [" + cmp.Or(f.Type, "output") + "]"
}

// ParseLatentRef turns a LatentRef back into the file it names.
func ParseLatentRef(ref string) (OutputFile, bool) {
	name, typ, ok := strings.Cut(strings.TrimSuffix(ref, "]"), " [")
	if !ok || name == "" || typ == "" {
		return OutputFile{}, false
	}
	dir, file := path.Split(name)
	return OutputFile{Filename: file, Subfolder: strings.TrimSuffix(dir, "/"), Type: typ}, file != ""
}

// RequiredNodes lists node classes and inputs the MiniMax graphs depend on.
var RequiredNodes = map[string][]string{
	"SaveLatent":                    {"samples", "filename_prefix"},
	"LoadLatent":                    {"latent"},
	"CLIPLoader":                    {"clip_name", "type"},
	"UNETLoader":                    {"unet_name", "weight_dtype"},
	"VAELoader":                     {"vae_name"},
	"MiniMaxMusic3TextEncode":       {"clip", "caption", "lyrics", "seed", "max_duration", "cfg_scale", "top_k"},
	"ConditioningZeroOut":           {"conditioning"},
	"EmptyMiniMaxMusic3LatentAudio": {"seconds", "batch_size"},
	"KSampler":                      {"model", "seed", "steps", "cfg", "sampler_name", "scheduler", "positive", "negative", "latent_image", "denoise"},
	"VAEDecodeAudioTiled":           {"samples", "vae", "tile_size", "overlap"},
	"SaveAudio":                     {"audio", "filename_prefix"},
}

// decodeNode decodes in overlapping tiles. A single full-length decode on
// Apple's MPS backend corrupts the whole song once the latent passes about
// 16,150 frames (3:07): a DC offset, fixed tones and no treble. Tiles of this
// size came out within -66 dB of a CPU decode of the same latent.
func decodeNode(samples string) Node {
	return Node{ClassType: "VAEDecodeAudioTiled", Inputs: map[string]any{
		"samples": Link(samples, 0), "vae": Link(NodeVAE, 0), "tile_size": 512, "overlap": 64,
	}}
}
