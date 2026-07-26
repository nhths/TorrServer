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

// ParseCaps extracts &v= and &a= from query.
//
// Examples (matching device_caps.js output):
//   v=h265:hw,h264:sw  a=aac,ac3
//   v=h264:hw          a=
//   "" / not present   → both empty
//
// Order-insensitive: returned slices are sorted so task-key digest is
// stable across requests that list the same codecs in different order.
func ParseCaps(q url.Values) (video []VideoCap, audio []string) {
	for _, v := range strings.Split(q.Get("v"), ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if i := strings.IndexByte(v, ':'); i >= 0 {
			codec := strings.ToLower(strings.TrimSpace(v[:i]))
			tier := parseTier(v[i+1:])
			if codec == "" || tier == TierNone {
				continue
			}
			video = append(video, VideoCap{Codec: codec, Tier: tier})
		}
	}
	for _, a := range strings.Split(q.Get("a"), ",") {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" {
			audio = append(audio, a)
		}
	}
	sortVideoCaps(video)
	sort.Strings(audio)
	return
}

// sortVideoCaps: by codec asc, then tier asc (Hw < Sw).
func sortVideoCaps(v []VideoCap) {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Codec != v[j].Codec {
			return v[i].Codec < v[j].Codec
		}
		return v[i].Tier < v[j].Tier
	})
}

// CapsDigest builds the per-task key suffix. Empty caps produce "" so
// legacy clients (no v/a) keep using hash-only task keys.
//
// The function sorts its inputs to make the digest order-independent:
// the same set of codecs (in any order) always produces the same
// digest. We sort copies to avoid mutating caller-owned slices.
func CapsDigest(video []VideoCap, audio []string) string {
	if len(video) == 0 && len(audio) == 0 {
		return ""
	}
	videoCopy := append([]VideoCap(nil), video...)
	audioCopy := append([]string(nil), audio...)
	sortVideoCaps(videoCopy)
	sort.Strings(audioCopy)

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

// AudioPassThrough reports whether the client can play the given
// source audio codec raw. Used by pipeline_gst.go to skip AAC
// transcoding when the client's a= list declares the codec.
//
// Codec names here are gst-discoverer short names as they appear in
// TrackInfo.Codec (always lowercased, e.g. "aac", "ac3", "eac3",
// "opus", "mp3", "flac", "vorbis"). Matches are substring-based to
// tolerate gst-discoverer variations like "audio/x-ac3" vs "ac3".
func AudioPassThrough(codec string, audioCaps []string) bool {
	codec = strings.ToLower(strings.TrimSpace(codec))
	if codec == "" || len(audioCaps) == 0 {
		return false
	}
	for _, c := range audioCaps {
		if c == "" {
			continue
		}
		if codec == c || strings.Contains(codec, c) || strings.Contains(c, codec) {
			return true
		}
	}
	return false
}