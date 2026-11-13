package setup

// Repo is the Hugging Face repository the model files come from.
const Repo = "Comfy-Org/MiniMax-Music-3"

// ModelFile is one file in Repo. Dir is both its folder in the repo and the
// ComfyUI models subfolder it belongs in.
type ModelFile struct {
	Dir    string
	Name   string
	Size   int64
	SHA256 string
	Label  string
	// Tag is a few words a list shows beside the file, such as who it is for.
	Tag         string
	Note        string
	Recommended bool
}

func (f ModelFile) RepoPath() string { return f.Dir + "/" + f.Name }

const (
	DirDiffusion   = "diffusion_models"
	DirTextEncoder = "text_encoders"
	DirVAE         = "vae"
)

// Manifest lists every model file in Repo, as listed on 2026-11-12.
var Manifest = []ModelFile{
	{
		Dir: DirDiffusion, Name: "minimax_music3_dit_fp16.safetensors", Size: 4914197682,
		SHA256: "45494a2b6b69af115902ff28eaf54118d19067aa54da01000f3e3efce7ba0e34",
		Label:  "DiT fp16", Note: "Recommended diffusion model.", Recommended: true,
	},
	{
		Dir: DirDiffusion, Name: "minimax_music3_dit_fp32.safetensors", Size: 9828345396,
		SHA256: "ab54b44bcc40ea49c44bd65fd86cade12adf1c5bd9a5aba63f0c68f438554d2d",
		Label:  "DiT fp32", Note: "Full precision, twice the size of fp16. Untested with songsmith.",
	},
	{
		Dir: DirDiffusion, Name: "minimax_music3_dit_int8_convrot.safetensors", Size: 2502161682,
		SHA256: "d6b959633e69899f99f3a92d6741c0fe79f26958a30811e50e372ef978b24d5f",
		Label:  "DiT int8", Tag: "for NVIDIA 16 GB cards",
		Note: "Targets NVIDIA cards with 16 GB (untested on NVIDIA). Untested with songsmith.",
	},
	{
		Dir: DirTextEncoder, Name: "minimax_music3_text_encoder_pruned_bf16.safetensors", Size: 16706629398,
		SHA256: "e81e469c92af324dc69b77e5a11179e22542a6d628f2b205c8cf6143e510d976",
		Label:  "Text encoder bf16 (pruned)", Note: "Recommended text encoder.", Recommended: true,
	},
	{
		Dir: DirTextEncoder, Name: "minimax_music3_text_encoder_pruned_int8_convrot.safetensors", Size: 9196611886,
		SHA256: "010b7416d2336a08c711bc22ee65849c9623069ddb7d89bec011a75699e52014",
		Label:  "Text encoder int8 (pruned)", Tag: "for NVIDIA 16 GB cards",
		Note: "Targets NVIDIA cards with 16 GB (untested on NVIDIA). On Apple Silicon it ran on the CPU in testing, about 50x slower.",
	},
	{
		Dir: DirTextEncoder, Name: "minimax_music3_text_encoder_bf16.safetensors", Size: 18472478038,
		SHA256: "9805d045978ce917cd1e6327b5cd8b85df1a46e4c69b61c9acf3eb29891c3958",
		Label:  "Text encoder bf16 (full)", Note: "Unpruned, larger than the pruned file. Untested with songsmith.",
	},
	{
		Dir: DirVAE, Name: "minimax_music3_dav.safetensors", Size: 216696128,
		SHA256: "2a32155b769be01445fcc2a8663b910fc9e1751e18dc1c3ec528064512d9ef0c",
		Label:  "VAE", Note: "The only VAE; required.", Recommended: true,
	},
}

// Lookup finds a manifest file by models subfolder and file name.
func Lookup(dir, name string) (ModelFile, bool) {
	for _, f := range Manifest {
		if f.Dir == dir && f.Name == name {
			return f, true
		}
	}
	return ModelFile{}, false
}

// Variants returns the manifest files that belong in one models subfolder.
func Variants(dir string) []ModelFile {
	var out []ModelFile
	for _, f := range Manifest {
		if f.Dir == dir {
			out = append(out, f)
		}
	}
	return out
}
