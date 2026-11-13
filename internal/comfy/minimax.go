package comfy

import "cmp"

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
		NodeDecode: {ClassType: "VAEDecodeAudio", Inputs: map[string]any{
			"samples": Link(NodeSampler, 0), "vae": Link(NodeVAE, 0),
		}},
		NodeSave: {ClassType: "SaveAudio", Inputs: map[string]any{
			"audio": Link(NodeDecode, 0), "filename_prefix": p.FilenamePrefix,
		}},
	}
}

// RequiredNodes lists node classes and inputs the MiniMax graph depends on.
var RequiredNodes = map[string][]string{
	"CLIPLoader":                    {"clip_name", "type"},
	"UNETLoader":                    {"unet_name", "weight_dtype"},
	"VAELoader":                     {"vae_name"},
	"MiniMaxMusic3TextEncode":       {"clip", "caption", "lyrics", "seed", "max_duration", "cfg_scale", "top_k"},
	"ConditioningZeroOut":           {"conditioning"},
	"EmptyMiniMaxMusic3LatentAudio": {"seconds", "batch_size"},
	"KSampler":                      {"model", "seed", "steps", "cfg", "sampler_name", "scheduler", "positive", "negative", "latent_image", "denoise"},
	"VAEDecodeAudio":                {"samples", "vae"},
	"SaveAudio":                     {"audio", "filename_prefix"},
}
