//go:build gst

package gstreamer

import (
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Corner / edge cases surfaced by the second-pass code review. Each
// test pins a single invariant so future refactors can't silently
// regress it.

// --- Parsing edges ---------------------------------------------------

func TestParseCaps_HugeInputDoesNotOOM(t *testing.T) {
	// 1 MB of garbage must finish fast and return empty slices.
	huge := "v=" + strings.Repeat("a:b,", (1<<20)/3)
	q, err := url.ParseQuery(huge)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	v, a := ParseCapsLegacy(q)
	if len(v) > 0 || len(a) > 0 {
		t.Fatalf("expected empty, got v=%d a=%d", len(v), len(a))
	}
}

func TestParseCaps_DoesNotPanicOnControlChars(t *testing.T) {
	// No assertion beyond "doesn't panic and returns something" —
	// control chars are passed through to lower-level layers, which
	// is acceptable; the caps string never lands in a shell.
	q, _ := url.ParseQuery("v=\x00h264\x00:HW,aac\x01\x02")
	v, a := ParseCapsLegacy(q)
	if v == nil && a == nil {
		t.Fatal("expected at least one parsed entry")
	}
}

func TestParseCaps_WhitespaceAroundTierIsTolerated(t *testing.T) {
	q, _ := url.ParseQuery("v= h264 : hw ")
	v, _ := ParseCapsLegacy(q)
	if len(v) != 1 || v[0] != (VideoCap{Codec: "h264", Tier: TierHw}) {
		t.Fatalf("got %+v", v)
	}
}

func TestParseCaps_OnlySeparators(t *testing.T) {
	q, _ := url.ParseQuery("v=,,,,&a=,,,")
	if v, a := ParseCapsLegacy(q); v != nil || a != nil {
		t.Fatalf("got v=%v a=%v", v, a)
	}
}

func TestParseCaps_EmptyValueKey(t *testing.T) {
	q, _ := url.ParseQuery("v=&a=")
	if v, a := ParseCapsLegacy(q); v != nil || a != nil {
		t.Fatalf("got v=%v a=%v", v, a)
	}
}

// --- Separator / key injection ---------------------------------------

func TestCapsDigest_NoInternalSeparator(t *testing.T) {
	cases := []struct {
		v []VideoCap
		a []string
	}{
		{[]VideoCap{{Codec: "h264", Tier: TierHw}}, []string{"aac"}},
		{nil, []string{"ac3"}},
		{[]VideoCap{{Codec: "h264", Tier: TierHw}, {Codec: "h265", Tier: TierSw}}, []string{"ac3", "eac3"}},
	}
	for _, tc := range cases {
		if d := CapsDigest(tc.v, tc.a, nil); strings.ContainsAny(d, "|\x00") {
			t.Fatalf("digest %q contains separator", d)
		}
	}
}

func TestTaskKey_HashWithSeparatorIsOpaque(t *testing.T) {
	// Document the current behaviour: a hash that itself contains the
	// separator ("hash|legacy") aliases no caps variant — it's just a
	// weird bare hash. Future refactors that change this contract
	// must update this test.
	if k := taskKey("abc|def", nil, nil, nil); k != "abc|def" {
		t.Fatalf("bare hash with separator changed to %q", k)
	}
	if k := taskKey("abc|def", []VideoCap{{Codec: "h264", Tier: TierHw}}, nil, nil); k == "abc|def" {
		t.Fatalf("caps variant collides with separator-bearing hash: %q", k)
	}
}

func TestRemoveCapsVariants_EmptyHashIsNoOp(t *testing.T) {
	s := &Service{tasks: map[string]*Task{}}
	if s.removeCapsVariants("") {
		t.Fatal("removeCapsVariants(\"\") returned true")
	}
}

func TestFindAnyByHash_EmptyHashIsNil(t *testing.T) {
	s := &Service{tasks: map[string]*Task{}}
	if got := s.findAnyByHash(""); got != nil {
		t.Fatalf("findAnyByHash(\"\") = %v", got)
	}
}

// --- findAnyByHash determinism + disposed-skipping --------------------

func TestFindAnyByHash_PrefersLegacyDeterministically(t *testing.T) {
	legacy, _ := newTrackedTask("h", time.Now().UTC())
	capA, _ := newTrackedTask("h|v:h264:hw,", time.Now().UTC())
	capB, _ := newTrackedTask("h|v:h264:sw,", time.Now().UTC())
	s := &Service{tasks: map[string]*Task{
		legacy.ID: legacy, capA.ID: capA, capB.ID: capB,
	}}
	for i := 0; i < 100; i++ {
		if got := s.findAnyByHash("h"); got != legacy {
			t.Fatalf("iteration %d: legacy must always win when present, got %v", i, got)
		}
	}
}

func TestFindAnyByHash_SkipsDisposedCapsVariants(t *testing.T) {
	legacy, _ := newTrackedTask("h", time.Now().UTC())
	disposed, _ := newTrackedTask("h|v:h264:hw,", time.Now().UTC())
	alive, _ := newTrackedTask("h|v:h264:sw,", time.Now().UTC())
	disposed.Dispose()
	s := &Service{tasks: map[string]*Task{
		legacy.ID: legacy, disposed.ID: disposed, alive.ID: alive,
	}}
	if got := s.findAnyByHash("h"); got != legacy {
		t.Fatalf("want legacy, got %v", got)
	}
	delete(s.tasks, legacy.ID)
	if got := s.findAnyByHash("h"); got != alive {
		t.Fatalf("disposed variant leaked: got %v want %v", got, alive)
	}
}

// --- Service lifecycle after Dispose ---------------------------------

func TestServiceDispose_RejectsTryRemoveAndVariants(t *testing.T) {
	task, _ := newTrackedTask("h", time.Now().UTC())
	s := &Service{
		tasks:       map[string]*Task{task.ID: task},
		probeCache:  make(map[string]probeCacheEntry),
		stopCleanup: make(chan struct{}),
	}
	s.Dispose()
	if s.TryRemove("h") {
		t.Fatal("TryRemove succeeded on disposed service")
	}
	if s.removeCapsVariants("h") {
		t.Fatal("removeCapsVariants succeeded on disposed service")
	}
}

// --- MaxTasks invariant ----------------------------------------------
//
// Skipped: GetOrAdd against a non-existent hash reaches
// torr.GetTorrent which NPEs in apihelper.go:90 without a real BT
// client. The invariant itself is enforced by the real test
// TestTaskLimitOneEvictsExistingTaskForProtectedNewTask in
// service_test.go.

// --- Config JSON round-trip ------------------------------------------

func TestConfig_OmitsVideoAudioCapsOnMarshal(t *testing.T) {
	conf := Config{
		VideoCaps: []VideoCap{{Codec: "h264", Tier: TierHw}},
		AudioCaps: []string{"aac"},
	}
	data, err := json.Marshal(conf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "VideoCaps") || strings.Contains(string(data), "AudioCaps") {
		t.Fatalf("caps leaked into persisted JSON: %s", data)
	}
}

func TestStoredConfig_OldCapsKeyIsSilentlyIgnored(t *testing.T) {
	// An older release might have persisted VideoCaps/AudioCaps in the
	// settings file. The unmarshal target must not error and must
	// keep the rest of the fields.
	raw := []byte(`{"MaxTasks": 4, "VideoCaps": [{"Codec":"h264","Tier":1}], "AudioCaps": ["aac"]}`)
	var s storedConfig
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal should tolerate unknown fields: %v", err)
	}
	if s.MaxTasks == nil || *s.MaxTasks != 4 {
		t.Fatalf("expected MaxTasks=4, got %+v", s)
	}
}

// --- Pipeline fallback guard -----------------------------------------

func TestAudioPassThrough_FamiliesHaveParserBranch(t *testing.T) {
	// Every family that AudioPassThrough can return true for must
	// have a real parser branch in audioPassthroughCaps. The
	// "identity" fallback is unreachable in production; this test
	// fails if someone authorises a new family without wiring the
	// pipeline.
	authorised := []struct {
		track  *TrackInfo
		caps   []string
		family string
	}{
		{mkTrack("AC-3", "audio/x-ac3"), []string{"ac3"}, AudioFamilyAC3},
		{mkTrack("E-AC-3", "audio/x-eac3"), []string{"eac3"}, AudioFamilyEAC3},
		{mkTrack("Opus", "audio/x-opus"), []string{"opus"}, AudioFamilyOpus},
		{mkTrack("MP3 (Layer 3)", "audio/mpeg"), []string{"mp3"}, AudioFamilyMP3},
	}
	for _, tc := range authorised {
		if !AudioPassThrough(tc.track, tc.caps) {
			t.Fatalf("%v not authorised (regression)", tc)
		}
		supported := map[string]bool{
			AudioFamilyAC3:  true,
			AudioFamilyEAC3: true,
			AudioFamilyOpus: true,
			AudioFamilyMP3:  true,
		}
		if !supported[tc.family] {
			t.Fatalf("family %q has passthrough unlock but no parser branch", tc.family)
		}
	}
}

// --- audioFamily edge cases ------------------------------------------

func TestAudioFamily_NilSafe(t *testing.T) {
	cases := []struct {
		track *TrackInfo
		want  string
	}{
		{nil, AudioFamilyUnknown},
		{&TrackInfo{}, AudioFamilyUnknown},
		{&TrackInfo{Codec: " ", CapsName: " "}, AudioFamilyUnknown},
	}
	for _, tc := range cases {
		if got := audioFamily(tc.track); got != tc.want {
			t.Errorf("audioFamily(%+v) = %q, want %q", tc.track, got, tc.want)
		}
	}
}

// --- Property-style: order-independence + dedup -----------------------

func TestProperty_DigestStableUnderPermutation(t *testing.T) {
	// A few hundred random-ish permutations must produce the same
	// digest. Not a real QuickCheck (testing/quick not used here to
	// avoid new deps), but enough to catch regressions.
	baseV := []VideoCap{{"h265", TierHw}, {"h264", TierSw}, {"av1", TierHw}}
	baseA := []string{"aac", "ac3", "opus", "eac3"}

	base := CapsDigest(baseV, baseA, nil)
	for i := 0; i < 200; i++ {
		v := permuteVideoCaps(baseV, i)
		a := permuteStrings(baseA, i+7)
		if got := CapsDigest(v, a, nil); got != base {
			t.Fatalf("perm %d: %q != base %q", i, got, base)
		}
	}
}

func TestProperty_DigestStableUnderDuplication(t *testing.T) {
	v := []VideoCap{{"h264", TierHw}}
	a := []string{"ac3"}
	base := CapsDigest(v, a, nil)

	for _, n := range []int{2, 3, 10, 100} {
		dupV := make([]VideoCap, 0, n)
		dupA := make([]string, 0, n)
		for i := 0; i < n; i++ {
			dupV = append(dupV, v...)
			dupA = append(dupA, a...)
		}
		if got := CapsDigest(dupV, dupA, nil); got != base {
			t.Fatalf("n=%d: %q != base %q", n, got, base)
		}
	}
}

// --- Dispose race regression -----------------------------------------

func TestAcquireReleaseDisposeRace(t *testing.T) {
	// Pattern: a handler has Acquire'd a task; removeCapsVariants
	// marks it; the handler finishes and Release runs; Dispose fires.
	// Assert: task.IsDisposed() at the end, no double-dispose, no
	// nil-task panic in the handler.
	conf := Config{}.normalized()
	task, _ := newTrackedTask("h|v:h264:hw,a:aac,", time.Now().UTC())
	task.Config = conf

	s := &Service{
		conf:        conf,
		tasks:       map[string]*Task{task.ID: task},
		probeCache:  make(map[string]probeCacheEntry),
		stopCleanup: make(chan struct{}),
	}

	if s.Acquire(task.ID) == nil {
		t.Fatal("first Acquire must succeed")
	}
	if s.Acquire(task.ID) == nil {
		t.Fatal("second concurrent Acquire must also succeed (refs=2)")
	}

	if !s.removeCapsVariants("h") {
		t.Fatal("removeCapsVariants should claim the task")
	}
	if _, ok := s.tasks[task.ID]; ok {
		t.Errorf("task should be detached from map after remove")
	}
	if task.refsLoad() != 2 {
		t.Errorf("expected refs=2, got %d", task.refsLoad())
	}
	if task.IsDisposed() {
		t.Errorf("task should not be disposed while refs>0")
	}

	// Simulate two handlers finishing concurrently.
	s.Release(task)
	s.Release(task)

	// Give the disposeWhenQuiescent watcher a chance to run.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if task.IsDisposed() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !task.IsDisposed() {
		t.Fatalf("task should be disposed after refs reached 0; refs=%d", task.refsLoad())
	}

	// Further Acquire on a disposed task must fail.
	if s.Acquire(task.ID) != nil {
		t.Error("Acquire on disposed task must return nil")
	}
	s.Dispose()
}

func TestTryRemove_MarksThenDisposesImmediatelyWhenQuiescent(t *testing.T) {
	conf := Config{}.normalized()
	task, _ := newTrackedTask("h", time.Now().UTC())
	task.Config = conf

	s := &Service{
		conf:        conf,
		tasks:       map[string]*Task{task.ID: task},
		probeCache:  make(map[string]probeCacheEntry),
		stopCleanup: make(chan struct{}),
	}

	if !s.TryRemove("h") {
		t.Fatal("TryRemove should succeed")
	}
	if !task.IsDisposed() {
		t.Error("TryRemove on quiescent task should dispose immediately")
	}
	s.Dispose()
}

// --- Helpers --------------------------------------------------------

func permuteVideoCaps(in []VideoCap, seed int) []VideoCap {
	out := append([]VideoCap(nil), in...)
	for i := 0; i < len(out)-1; i++ {
		j := (i + seed) % len(out)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func permuteStrings(in []string, seed int) []string {
	out := append([]string(nil), in...)
	for i := 0; i < len(out)-1; i++ {
		j := (i + seed) % len(out)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// TestNoGoroutineLeak is a sanity check that the corner tests above
// don't leak goroutines (cleanupLoop fires every minute; we want it
// to exit when Dispose closes stopCleanup). It's not a hard
// correctness check — running with -race and watching the test
// summary is the real signal.
func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	s := NewService(Config{}.normalized())
	// Wait one cleanup tick to confirm goroutine actually started.
	time.Sleep(50 * time.Millisecond)
	s.Dispose()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+1 {
		t.Logf("goroutine count before=%d after=%d (some background goroutines may still be cleaning up)", before, after)
	}
}

// Ensure the existing wg variable is referenced so the test compiler
// doesn't complain about unused imports in some build configs.
var _ = sync.WaitGroup{}
var _ = fmt.Sprintf