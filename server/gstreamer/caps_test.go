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

func TestCapsDigest_EmptyForEmptyCaps(t *testing.T) {
	if got := CapsDigest(nil, nil); got != "" {
		t.Fatalf("digest for empty caps = %q, want \"\"", got)
	}
	if got := CapsDigest([]VideoCap{}, []string{}); got != "" {
		t.Fatalf("digest for empty caps = %q, want \"\"", got)
	}
}

func TestAudioPassThrough(t *testing.T) {
	caps := []string{"aac", "ac3", "eac3", "opus", "mp3"}
	cases := []struct {
		codec string
		want  bool
	}{
		{"ac3", true},
		{"audio/x-ac3", true},
		{"eac3", true},
		{"opus", true},
		{"audio/x-opus", true},
		{"mp3", true},
		{"dts", false},
		{"flac", false},
		{"aac", true}, // aac passthrough always allowed if listed
		{"", false},
	}
	for _, tc := range cases {
		if got := AudioPassThrough(tc.codec, caps); got != tc.want {
			t.Errorf("AudioPassThrough(%q) = %v, want %v", tc.codec, got, tc.want)
		}
	}

	// empty caps → nothing passes
	if AudioPassThrough("ac3", nil) {
		t.Error("empty caps should not pass through ac3")
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

func TestRemoveCapsVariants_NoTasks(t *testing.T) {
	s := &Service{tasks: map[string]*Task{}}
	if s.removeCapsVariants("missing") {
		t.Fatal("removeCapsVariants returned true on empty map")
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
		t.Errorf("unrelated keep.ID got removed")
	}
	if _, ok := s.tasks[keepOther.ID]; !ok {
		t.Errorf("unrelated other got removed")
	}
	if !dropA.IsDisposed() || !dropB.IsDisposed() {
		t.Error("matched tasks not disposed")
	}
}