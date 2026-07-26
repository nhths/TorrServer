//go:build gst

package gstreamer

import (
	"net/url"
	"sort"
	"strings"
)

// VideoCap describes one video codec entry declared by the client.
// Tier is informational (hw/sw) — pipeline_gst.go currently ignores it
// (we always transcode to h264 baseline for browser compat) but keeps
// it so future work can short-circuit passthrough for hw-rated codecs.
type VideoCap struct {
	Codec string // "h264", "h265", "av1", "vp9", ...
	Tier  Tier   // Hw | Sw
}

// Tier is the decode-capability tier reported by the client.
type Tier uint8

const (
	TierNone Tier = iota // codec not requested at all
	TierHw               // hardware decode / passthrough
	TierSw               // software decode only
)

func parseTier(s string) Tier {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hw":
		return TierHw
	case "sw":
		return TierSw
	default:
		return TierNone
	}
}

func (t Tier) String() string {
	switch t {
	case TierHw:
		return "hw"
	case TierSw:
		return "sw"
	default:
		return ""
	}
}

// audioFamily maps source audio (TrackInfo.CapsName / Codec) and
// client declarations (string from &a=) to a single canonical token.
// The token drives every downstream decision: passthrough eligibility,
// gst parse element, RFC 6381 codec string.
//
// Note: "aac" and "mp3" both end up as audio/mpeg in gst-discoverer's
// CapsName; we resolve them via the raw Codec field when needed
// (see audioFamily).
const (
	AudioFamilyAAC    = "aac"
	AudioFamilyAC3    = "ac3"
	AudioFamilyEAC3   = "eac3"
	AudioFamilyOpus   = "opus"
	AudioFamilyMP3    = "mp3"
	AudioFamilyVorbis = "vorbis"
	AudioFamilyFLAC   = "flac"
	AudioFamilyUnknown = ""
)

// audioFamily returns the canonical family for a source audio track.
// Falls back to codec substring only when CapsName is empty.
func audioFamily(track *TrackInfo) string {
	if track == nil {
		return AudioFamilyUnknown
	}
	caps := strings.ToLower(strings.TrimSpace(track.CapsName))
	codec := strings.ToLower(track.Codec)

	switch caps {
	case "audio/x-eac3":
		return AudioFamilyEAC3
	case "audio/x-ac3":
		return AudioFamilyAC3
	case "audio/x-opus":
		return AudioFamilyOpus
	case "audio/x-vorbis":
		return AudioFamilyVorbis
	case "audio/x-flac":
		return AudioFamilyFLAC
	case "audio/mpeg":
		// CapsName conflates AAC and MP3 — disambiguate via codec.
		if strings.Contains(codec, "aac") || strings.Contains(codec, "mp4a") {
			return AudioFamilyAAC
		}
		if strings.Contains(codec, "mp3") || strings.Contains(codec, "layer 3") {
			return AudioFamilyMP3
		}
		return AudioFamilyAAC
	}
	// CapsName missing — last-ditch substring on codec.
	switch {
	case strings.Contains(codec, "eac3") || strings.Contains(codec, "e-ac-3") || strings.Contains(codec, "e-ac3"):
		return AudioFamilyEAC3
	case strings.Contains(codec, "ac3") || strings.Contains(codec, "ac-3") || strings.Contains(codec, "a/52"):
		return AudioFamilyAC3
	case strings.Contains(codec, "opus"):
		return AudioFamilyOpus
	case strings.Contains(codec, "aac") || strings.Contains(codec, "mp4a"):
		return AudioFamilyAAC
	case strings.Contains(codec, "mp3") || strings.Contains(codec, "layer 3"):
		return AudioFamilyMP3
	}
	return AudioFamilyUnknown
}

// normalizeAudioCapToken canonicalizes a client-declared audio token
// ("AAC", "audio/x-ac3", "e-ac3") to one of the AudioFamily* tokens.
// Returns AudioFamilyUnknown for unrecognised tokens.
func normalizeAudioCapToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "aac", "audio/mpeg", "audio/aac", "mp4a":
		return AudioFamilyAAC
	case "ac3", "a/52", "audio/x-ac3", "ac-3":
		return AudioFamilyAC3
	case "eac3", "audio/x-eac3", "e-ac-3", "ec-3":
		return AudioFamilyEAC3
	case "opus", "audio/x-opus":
		return AudioFamilyOpus
	case "mp3", "audio/mpeg,layer=3", "layer 3":
		return AudioFamilyMP3
	}
	return AudioFamilyUnknown
}

// AudioPassThrough reports whether the source audio track should skip
// AAC transcoding because the client declared support for this family
// in &a=. Unknown family tokens in audioCaps are ignored (they don't
// unlock passthrough — see PipelineGstPassthrough for the reject list).
func AudioPassThrough(track *TrackInfo, audioCaps []string) bool {
	family := audioFamily(track)
	if family == AudioFamilyAAC || family == AudioFamilyUnknown {
		// AAC already passthrough via existing branch; unknown
		// families get transcoded so mp4mux gets a sane payload.
		return false
	}
	for _, raw := range audioCaps {
		if normalizeAudioCapToken(raw) == family {
			return true
		}
	}
	return false
}

// ParseCaps extracts &v= and &a= from query. Returns sorted, deduped
// slices. Unknown audio tokens are dropped (no passthrough unlock,
// no task-key pollution).
//
// Examples (matching device_caps.js output):
//   v=h265:hw,h264:sw  a=aac,ac3
//   v=h264:hw          a=
//   "" / not present   → both empty
func ParseCaps(q url.Values) (video []VideoCap, audio []string) {
	for _, v := range strings.Split(q.Get("v"), ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		i := strings.IndexByte(v, ':')
		if i < 0 {
			continue
		}
		codec := strings.ToLower(strings.TrimSpace(v[:i]))
		tier := parseTier(v[i+1:])
		if codec == "" || tier == TierNone {
			continue
		}
		video = append(video, VideoCap{Codec: codec, Tier: tier})
	}
	for _, a := range strings.Split(q.Get("a"), ",") {
		fam := normalizeAudioCapToken(a)
		if fam == AudioFamilyUnknown {
			continue
		}
		audio = append(audio, fam)
	}

	// Dedup + sort: equivalent sets (any order, any dup count) produce
	// the same task key.
	video = dedupVideoCaps(video)
	audio = dedupStrings(audio)
	sortVideoCaps(video)
	sort.Strings(audio)
	return
}

func dedupVideoCaps(v []VideoCap) []VideoCap {
	if len(v) == 0 {
		return v
	}
	seen := make(map[VideoCap]struct{}, len(v))
	out := v[:0]
	for _, c := range v {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func dedupStrings(s []string) []string {
	if len(s) == 0 {
		return s
	}
	seen := make(map[string]struct{}, len(s))
	out := s[:0]
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func sortVideoCaps(v []VideoCap) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Codec != v[j].Codec {
			return v[i].Codec < v[j].Codec
		}
		return v[i].Tier < v[j].Tier
	})
}

// CapsDigest builds the per-task key suffix. Empty caps produce "" so
// legacy clients (no v/a) keep using hash-only task keys. Order-
// independent (caller may pass anything; we sort copies).
func CapsDigest(video []VideoCap, audio []string) string {
	// Dedup defensively: callers may pass raw slices without going
	// through ParseCaps (e.g. tests, or callers constructing caps
	// in-place). Order-independent set semantics are part of the
	// contract.
	videoCopy := dedupVideoCaps(append([]VideoCap(nil), video...))
	audioCopy := dedupStrings(append([]string(nil), audio...))
	sortVideoCaps(videoCopy)
	sort.Strings(audioCopy)

	if len(videoCopy) == 0 && len(audioCopy) == 0 {
		return ""
	}
	var b strings.Builder
	for _, vc := range videoCopy {
		b.WriteString("v:")
		b.WriteString(vc.Codec)
		b.WriteByte(':')
		b.WriteString(vc.Tier.String())
		b.WriteByte(',')
	}
	for _, ac := range audioCopy {
		b.WriteString("a:")
		b.WriteString(ac)
		b.WriteByte(',')
	}
	return b.String()
}
