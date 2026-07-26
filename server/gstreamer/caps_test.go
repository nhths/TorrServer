//go:build gst

package gstreamer

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestParseCaps(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantVideo []VideoCap
		wantAudio []string
	}{
		{
			name:      "empty",
			query:     "",
			wantVideo: nil,
			wantAudio: nil,
		},
		{
			name:      "video only, two codecs",
			query:     "v=h265:hw,h264:sw",
			wantVideo: []VideoCap{{"h264", TierSw}, {"h265", TierHw}},
			wantAudio: nil,
		},
		{
			name:      "audio only",
			query:     "a=aac,ac3",
			wantVideo: nil,
			wantAudio: []string{"aac", "ac3"},
		},
		{
			name:      "video and audio",
			query:     "v=h264:hw&a=aac,ac3,eac3",
			wantVideo: []VideoCap{{"h264", TierHw}},
			wantAudio: []string{"aac", "ac3", "eac3"},
		},
		{
			name:      "order-insensitive video",
			query:     "v=h264:sw,h265:hw",
			wantVideo: []VideoCap{{"h264", TierSw}, {"h265", TierHw}},
			wantAudio: nil,
		},
		{
			name:      "case-insensitive",
			query:     "v=H265:HW&a=AAC,AC3",
			wantVideo: []VideoCap{{"h265", TierHw}},
			wantAudio: []string{"aac", "ac3"},
		},
		{
			name:      "unknown tier dropped",
			query:     "v=h264:no,h265:hw",
			wantVideo: []VideoCap{{"h265", TierHw}},
			wantAudio: nil,
		},
		{
			name:      "empty codec dropped",
			query:     "v=:hw,h264:sw",
			wantVideo: []VideoCap{{"h264", TierSw}},
			wantAudio: nil,
		},
		{
			name:      "single-codec audio lowercase",
			query:     "a=Opus",
			wantVideo: nil,
			wantAudio: []string{"opus"},
		},
		{
			name:      "no caps",
			query:     "index=1&audio=0",
			wantVideo: nil,
			wantAudio: nil,
		},
		{
			name:      "duplicate audio tokens dedup",
			query:     "a=ac3,ac3,ac3",
			wantVideo: nil,
			wantAudio: []string{"ac3"},
		},
		{
			name:      "duplicate video entries dedup",
			query:     "v=h264:hw,h264:hw,h265:sw",
			wantVideo: []VideoCap{{"h264", TierHw}, {"h265", TierSw}},
			wantAudio: nil,
		},
		{
			name:      "unknown audio token dropped (no passthrough unlock)",
			query:     "a=flac,dts,ac3",
			wantVideo: nil,
			wantAudio: []string{"ac3"},
		},
		{
			name:      "audio token variants canonicalised",
			query:     "a=audio/x-ac3,e-ac-3,ec-3,a/52,layer 3",
			wantVideo: nil,
			wantAudio: []string{"ac3", "eac3", "mp3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.query, err)
			}
			gotVideo, gotAudio := ParseCaps(q)
			if !reflect.DeepEqual(gotVideo, tc.wantVideo) {
				t.Errorf("video = %#v, want %#v", gotVideo, tc.wantVideo)
			}
			if !reflect.DeepEqual(gotAudio, tc.wantAudio) {
				t.Errorf("audio = %#v, want %#v", gotAudio, tc.wantAudio)
			}
		})
	}
}

func TestCapsDigest_StableForSameSet(t *testing.T) {
	a := CapsDigest([]VideoCap{{"h265", TierHw}, {"h264", TierSw}}, []string{"aac", "ac3"})
	b := CapsDigest([]VideoCap{{"h264", TierSw}, {"h265", TierHw}}, []string{"ac3", "aac"})
	if a != b {
		t.Fatalf("digest not stable: %q vs %q", a, b)
	}
	if a == "" {
		t.Fatal("digest empty for non-empty caps")
	}
}

func TestCapsDigest_StableForDuplicates(t *testing.T) {
	a := CapsDigest([]VideoCap{{"h264", TierHw}}, []string{"ac3"})
	b := CapsDigest([]VideoCap{{"h264", TierHw}, {"h264", TierHw}}, []string{"ac3", "ac3"})
	if a != b {
		t.Fatalf("digest not stable for duplicates: %q vs %q", a, b)
	}
}

func TestCapsDigest_EmptyForEmptyCaps(t *testing.T) {
	if got := CapsDigest(nil, nil); got != "" {
		t.Fatalf("digest for empty caps = %q, want \"\"", got)
	}
	if got := CapsDigest([]VideoCap{}, []string{}); got != "" {
		t.Fatalf("digest for empty caps = %q, want \"\"", got)
	}
}

func mkTrack(codec, capsName string) *TrackInfo {
	return &TrackInfo{Type: "audio", Codec: codec, CapsName: capsName}
}

func TestAudioPassThrough_OnlyAuthorisedFamilies(t *testing.T) {
	cases := []struct {
		name  string
		track *TrackInfo
		caps  []string
		want  bool
	}{
		{"ac3 source via capsName", mkTrack("AC-3", "audio/x-ac3"), []string{"ac3"}, true},
		{"eac3 source via capsName", mkTrack("E-AC-3", "audio/x-eac3"), []string{"eac3"}, true},
		{"opus source via capsName", mkTrack("Opus", "audio/x-opus"), []string{"opus"}, true},
		{"mp3 source via codec (capsName is audio/mpeg shared with aac)", mkTrack("MP3 (Layer 3)", "audio/mpeg"), []string{"mp3"}, true},
		{"aac source always false (existing branch handles)", mkTrack("AAC LC", "audio/mpeg"), []string{"aac"}, false},
		{"flac source rejected (not in caps)", mkTrack("FLAC", "audio/x-flac"), []string{"ac3"}, false},
		{"dts source rejected", mkTrack("DTS", "audio/x-dts"), []string{"ac3", "dts"}, false},
		{"empty capsName → codec fallback", mkTrack("audio/x-ac3", ""), []string{"ac3"}, true},
		{"eac3 source must not satisfy ac3 declaration", mkTrack("E-AC-3", "audio/x-eac3"), []string{"ac3"}, false},
		{"ac3 source must not satisfy eac3 declaration", mkTrack("AC-3", "audio/x-ac3"), []string{"eac3"}, false},
		{"empty track", nil, []string{"ac3"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AudioPassThrough(tc.track, tc.caps); got != tc.want {
				t.Errorf("AudioPassThrough(%v, %v) = %v, want %v", tc.track, tc.caps, got, tc.want)
			}
		})
	}
}

func TestAudioPassThrough_NoCapsNoPass(t *testing.T) {
	track := mkTrack("AC-3", "audio/x-ac3")
	if AudioPassThrough(track, nil) {
		t.Error("empty caps should not unlock passthrough")
	}
}

func TestAudioFamily(t *testing.T) {
	cases := []struct {
		codec, capsName, want string
	}{
		{"AAC LC", "audio/mpeg", AudioFamilyAAC},
		{"MP3 (Layer 3)", "audio/mpeg", AudioFamilyMP3},
		{"AC-3", "audio/x-ac3", AudioFamilyAC3},
		{"A/52", "audio/x-ac3", AudioFamilyAC3},
		{"E-AC-3", "audio/x-eac3", AudioFamilyEAC3},
		{"Opus", "audio/x-opus", AudioFamilyOpus},
		{"Vorbis", "audio/x-vorbis", AudioFamilyVorbis},
		{"FLAC", "audio/x-flac", AudioFamilyFLAC},
		{"DTS", "audio/x-dts", AudioFamilyUnknown},
		{"", "", AudioFamilyUnknown},
	}
	for _, tc := range cases {
		got := audioFamily(mkTrack(tc.codec, tc.capsName))
		if got != tc.want {
			t.Errorf("audioFamily(%q, %q) = %q, want %q", tc.codec, tc.capsName, got, tc.want)
		}
	}
}

func TestTaskKey_LegacyKeepsHash(t *testing.T) {
	if got := taskKey("hash", nil, nil); got != "hash" {
		t.Fatalf("taskKey(empty caps) = %q, want \"hash\"", got)
	}
	if got := taskKey("hash", []VideoCap{}, []string{}); got != "hash" {
		t.Fatalf("taskKey(no caps) = %q, want \"hash\"", got)
	}
}

func TestTaskKey_DistinctForDistinctCaps(t *testing.T) {
	a := taskKey("h", []VideoCap{{"h264", TierHw}}, []string{"aac"})
	b := taskKey("h", []VideoCap{{"h264", TierSw}}, []string{"aac"})
	if a == b {
		t.Fatalf("distinct caps produced same key: %q", a)
	}
}

func TestTaskKey_SameForOrderIndependentCaps(t *testing.T) {
	a := taskKey("h", []VideoCap{{"h265", TierHw}, {"h264", TierSw}}, []string{"aac", "ac3"})
	b := taskKey("h", []VideoCap{{"h264", TierSw}, {"h265", TierHw}}, []string{"ac3", "aac"})
	if a != b {
		t.Fatalf("order-dependent key: %q vs %q", a, b)
	}
}

func TestFindAnyByHash_LegacyAndCapsVariants(t *testing.T) {
	legacy, _ := newTrackedTask("hash", time.Now().UTC())
	capA, _ := newTrackedTask("hash|v:h264:hw,a:aac,", time.Now().UTC())
	other, _ := newTrackedTask("other", time.Now().UTC())

	s := &Service{tasks: map[string]*Task{
		legacy.ID: legacy,
		capA.ID:   capA,
		other.ID:  other,
	}}
	if got := s.findAnyByHash("hash"); got != legacy {
		t.Errorf("findAnyByHash(h) = %v, want legacy", got)
	}

	// Remove the legacy; should fall back to the caps variant.
	delete(s.tasks, legacy.ID)
	if got := s.findAnyByHash("hash"); got != capA {
		t.Errorf("findAnyByHash(h) without legacy = %v, want capA", got)
	}

	if got := s.findAnyByHash("other"); got != other {
		t.Errorf("findAnyByHash(other) = %v, want other", got)
	}
	if got := s.findAnyByHash("missing"); got != nil {
		t.Errorf("findAnyByHash(missing) = %v, want nil", got)
	}
}

func TestRemoveCapsVariants_DropsOnlyMatching(t *testing.T) {
	keep, _ := newTrackedTask("hash", time.Now().UTC())
	keepOther, _ := newTrackedTask("other", time.Now().UTC())
	dropA, _ := newTrackedTask("hash|v:h264:hw,a:aac,", time.Now().UTC())
	dropB, _ := newTrackedTask("hash|v:h264:sw,a:aac,ac3,", time.Now().UTC())

	s := &Service{tasks: map[string]*Task{
		keep.ID:      keep,
		keepOther.ID: keepOther,
		dropA.ID:     dropA,
		dropB.ID:     dropB,
	}}
	if !s.removeCapsVariants("hash") {
		t.Fatal("removeCapsVariants returned false despite matches")
	}
	if _, ok := s.tasks[dropA.ID]; ok {
		t.Errorf("dropA still in map")
	}
	if _, ok := s.tasks[dropB.ID]; ok {
		t.Errorf("dropB still in map")
	}
	if _, ok := s.tasks[keep.ID]; !ok {
		t.Errorf("legacy keep.ID got removed (it shouldn't — removeCapsVariants only matches hash| prefix)")
	}
	if _, ok := s.tasks[keepOther.ID]; !ok {
		t.Errorf("unrelated other got removed")
	}
	if !dropA.IsDisposed() || !dropB.IsDisposed() {
		t.Error("matched tasks not disposed")
	}
}

func TestRemoveCapsVariants_NoTasks(t *testing.T) {
	s := &Service{tasks: map[string]*Task{}}
	if s.removeCapsVariants("missing") {
		t.Fatal("removeCapsVariants returned true on empty map")
	}
}