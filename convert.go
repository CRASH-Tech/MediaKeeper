package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// What every conversion shares — the web player's streams, both kinds of
// HLS, the Jellyfin apps' playback: ffmpeg options that turn a video into
// H.264 and stereo AAC, which every player takes.
//
// An H.264 track (8-bit, progressive) is copied, which costs almost
// nothing. Anything else is re-encoded: on the processor with libx264, or —
// when the settings ask for it and the card works — on a graphics card
// (VAAPI for Intel and AMD, Quick Sync, NVENC). A graphics card that fails
// on a file is not used for that file again; the processor takes over.

// convertOptions say what a conversion is for.
type convertOptions struct {
	audio int  // which audio track (ffmpeg's 0:a:N)
	hls   bool // a key frame every hlsSegmentSeconds, where segments are cut
	burn  int  // a picture subtitle track (ffmpeg's 0:s:N) burned in; -1 for none
}

// conversion is a set of ffmpeg options: those before -i (devices, decoding
// on the card) and those after it.
type conversion struct {
	input, output []string
	copied        bool   // the video track is copied, not re-encoded
	hw            string // the card's method ("vaapi", "qsv", "nvenc"); "" for the processor
}

func (s *Server) convertArgs(it *CatItem, o convertOptions) conversion {
	info := s.lib.Probe(it)
	v := info.stream("video")
	c := conversion{output: []string{"-map", fmt.Sprintf("0:a:%d?", max(o.audio, 0)), "-sn", "-dn", "-map_chapters", "-1"}}
	audio := []string{"-c:a", "aac", "-ac", "2", "-b:a", "192k"}

	if o.burn < 0 && v != nil && v.Codec == "h264" && !strings.Contains(v.Profile, "10") && !v.Interlaced() {
		c.copied = true
		c.output = append([]string{"-map", "0:v:0"}, append(c.output, "-c:v", "copy")...)
		c.output = append(c.output, audio...)
		return c
	}

	interlaced := v != nil && v.Interlaced()
	hw := s.hw
	if hw != nil && s.hwFailed(it.Path) {
		hw = nil
	}
	var video []string // the video filter chain and encoder
	switch {
	case hw == nil:
		video = s.cpuVideo(v, interlaced, o.burn)
	default:
		c.hw = hw.method
		var input []string
		input, video = hw.video(interlaced, o.burn)
		c.input = input
	}
	if o.hls {
		video = append(video, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", hlsSegmentSeconds))
		if c.hw == "nvenc" {
			video = append(video, "-forced-idr", "1")
		}
	}
	c.output = append(append(video, c.output...), audio...)
	return c
}

// cpuVideo re-encodes on the processor.
func (s *Server) cpuVideo(v *streamInfo, interlaced bool, burn int) []string {
	var args []string
	filters := "scale='min(1920,iw)':-2"
	if interlaced {
		// One frame per pair of fields, at a steady rate: such files often
		// carry uneven time stamps as well.
		filters = "bwdif=mode=send_frame," + filters
		if rate := v.FieldRate; rate > 0 {
			if rate > 31 {
				rate /= 2
			}
			args = append(args, "-r", strconv.FormatFloat(rate, 'f', 3, 64))
		}
	}
	args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p")
	if burn >= 0 {
		// The subtitles are drawn over the picture before it is scaled:
		// they are made for the film's own size.
		before := ""
		if interlaced {
			before = "bwdif=mode=send_frame,"
		}
		chain := fmt.Sprintf("[0:v:0]%snull[base];[base][0:s:%d]overlay=eof_action=pass,scale='min(1920,iw)':-2[v]", before, burn)
		return append([]string{"-filter_complex", chain, "-map", "[v]"}, args...)
	}
	return append([]string{"-map", "0:v:0", "-vf", filters}, args...)
}

// hwAccel is a graphics card that converts video.
type hwAccel struct {
	method string // vaapi, qsv or nvenc
	device string // /dev/dri/renderD128 for vaapi and qsv
}

// hwMethods are the ways known, in the order "auto" tries them.
var hwMethods = []string{"vaapi", "qsv", "nvenc"}

// video gives the options of a re-encoding on the card. The frames stay on
// the card from decoding to encoding where it can decode the file; where it
// cannot, ffmpeg decodes on the processor and the frames are uploaded
// ("format=…|vaapi,hwupload" takes either). Burned-in subtitles are drawn on
// the processor, then the frames go to the card for scaling and encoding.
func (h *hwAccel) video(interlaced bool, burn int) (input, args []string) {
	const width = "w='min(1920,iw)':h=-2"
	switch h.method {
	case "vaapi":
		input = []string{"-init_hw_device", "vaapi=va:" + h.device, "-filter_hw_device", "va"}
		chain := "format=nv12|vaapi,hwupload,"
		if burn < 0 {
			input = append(input, "-hwaccel", "vaapi", "-hwaccel_device", "va", "-hwaccel_output_format", "vaapi")
			if interlaced {
				chain += "deinterlace_vaapi,"
			}
		}
		chain += "scale_vaapi=" + width + ":format=nv12"
		args = []string{"-c:v", "h264_vaapi", "-qp", "23"}
		return input, append(h.chain(chain, interlaced, burn), args...)
	case "qsv":
		input = []string{"-init_hw_device", "vaapi=va:" + h.device, "-init_hw_device", "qsv=qs@va", "-filter_hw_device", "qs"}
		chain := "format=nv12|qsv,hwupload=extra_hw_frames=64,"
		vpp := "vpp_qsv=" + width + ":format=nv12"
		if burn < 0 {
			input = append(input, "-hwaccel", "qsv", "-hwaccel_output_format", "qsv")
			if interlaced {
				vpp += ":deinterlace=2"
			}
		}
		args = []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-global_quality", "23", "-look_ahead", "0"}
		return input, append(h.chain(chain+vpp, interlaced, burn), args...)
	default: // nvenc
		chain := "format=nv12|cuda,hwupload_cuda,"
		if burn < 0 {
			input = []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
			if interlaced {
				chain += "yadif_cuda,"
			}
		}
		chain += "scale_cuda=" + width + ":format=nv12"
		args = []string{"-c:v", "h264_nvenc", "-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", "0"}
		return input, append(h.chain(chain, interlaced, burn), args...)
	}
}

// chain wraps the card's filters: with burned-in subtitles, the overlay
// (and deinterlacing) come first, on the processor.
func (h *hwAccel) chain(onCard string, interlaced bool, burn int) []string {
	if burn < 0 {
		return []string{"-map", "0:v:0", "-vf", onCard}
	}
	before := ""
	if interlaced {
		before = "bwdif=mode=send_frame,"
	}
	return []string{"-filter_complex",
		fmt.Sprintf("[0:v:0]%snull[base];[base][0:s:%d]overlay=eof_action=pass,%s[v]", before, burn, onCard), "-map", "[v]"}
}

// detectHW sets up the graphics card the settings ask for: "auto" tries
// every method, a method's name tries only that one ("vaapi" may name its
// device: "vaapi:/dev/dri/renderD129"); "", "none" or "off" use the
// processor. A card is used only if a short test conversion works.
func detectHW(ffmpeg, choice string, log func(string, ...any)) *hwAccel {
	choice = strings.ToLower(strings.TrimSpace(choice))
	if ffmpeg == "" || choice == "" || choice == "none" || choice == "off" || choice == "cpu" {
		return nil
	}
	method, device, _ := strings.Cut(choice, ":")
	candidates := []string{method}
	if method == "auto" {
		candidates = hwMethods
	}
	for _, m := range candidates {
		h := &hwAccel{method: m, device: device}
		if m == "vaapi" || m == "qsv" {
			if h.device == "" {
				nodes, _ := filepath.Glob("/dev/dri/renderD*")
				if len(nodes) == 0 {
					if method != "auto" {
						log("hardware conversion: no /dev/dri/renderD* device for %s — converting on the processor", m)
					}
					continue
				}
				h.device = nodes[0]
			}
		} else if m != "nvenc" {
			log("hardware conversion: unknown method %q (auto, vaapi, qsv, nvenc, none) — converting on the processor", m)
			return nil
		}
		if out, err := h.test(ffmpeg); err != nil {
			if method != "auto" {
				log("hardware conversion with %s does not work (%s) — converting on the processor", m, out)
			}
			continue
		}
		log("hardware conversion: %s%s", m, map[bool]string{true: " on " + h.device, false: ""}[h.device != ""])
		return h
	}
	if method == "auto" {
		log("hardware conversion: no graphics card that works — converting on the processor")
	}
	return nil
}

// test converts a second of a test picture the way films will be.
func (h *hwAccel) test(ffmpeg string) (string, error) {
	input, video := h.video(false, -1)
	args := append([]string{"-v", "error"}, input...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=d=1:s=640x360:r=25,format=yuv420p")
	args = append(args, video...)
	args = append(args, "-f", "null", "-")
	out, err := exec.Command(ffmpeg, args...).CombinedOutput()
	return lastLine(string(out)), err
}

// hwFailures remembers the files a graphics card could not convert.
type hwFailures struct {
	mu    sync.Mutex
	paths map[string]bool
}

func (s *Server) hwFailed(path string) bool {
	s.hwBad.mu.Lock()
	defer s.hwBad.mu.Unlock()
	return s.hwBad.paths[path]
}

// hwGaveUp notes that the card failed on a file: the next conversion of it
// is done on the processor.
func (s *Server) hwGaveUp(path, why string) {
	s.hwBad.mu.Lock()
	if s.hwBad.paths == nil {
		s.hwBad.paths = map[string]bool{}
	}
	first := !s.hwBad.paths[path]
	s.hwBad.paths[path] = true
	s.hwBad.mu.Unlock()
	if first {
		s.log("hardware conversion failed on %s (%s): it is converted on the processor from now on", filepath.Base(path), why)
	}
}

// hwChoice is the hardware conversion asked for: -hwaccel, else
// MEDIAKEEPER_HWACCEL, else the settings.
func hwChoice(flag, setting string) string {
	return firstNonEmpty(flag, os.Getenv("MEDIAKEEPER_HWACCEL"), setting)
}
