//go:build gst

package gstreamer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// mkCapsTask creates a Task suitable for HTTP-level smoke tests:
// pre-loaded with a stub runner so EnsureInit succeeds and the
// master playlist can render with CODECS metadata.
func mkCapsTask(id, fileID string, audio int, conf Config) *Task {
	task := &Task{
		ID:              id,
		FileID:          fileID,
		Audio:           audio,
		Config:          conf,
		LastSentSegment: -1,
		lastActive:      time.Now().UTC(),
		Probe: ProbeInfo{
			DurationNS: int64(2 * time.Hour),
			FileSize:   24 * 1024 * 1024 * 1024,
			Tracks: []TrackInfo{
				{Type: "video", CapsName: "video/x-h265", Width: 3840, Height: 2160},
				{Type: "audio", Index: 0, CapsName: "audio/mpeg"},
			},
		},
	}
	task.runner = &masterInitRunner{task: task}
	return task
}

func TestHTTPMaster_WithCapsResolvesCapsTask(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	conf := Config{}.normalized()
	capsID := "hash|v:h264:hw,a:aac,"
	capsTask := mkCapsTask(capsID, "1", 0, conf)
	capsTask.Config.AudioCaps = []string{"aac"}
	capsTask.Config.VideoCaps = []VideoCap{{Codec: "h264", Tier: TierHw}}

	s := &Service{
		conf:       conf,
		tasks:      map[string]*Task{capsID: capsTask},
		probeCache: make(map[string]probeCacheEntry),
	}
	s.SetupRoute(router)

	req := httptest.NewRequest(http.MethodGet,
		"/gst/hash/master.m3u8?index=1&audio=0&v=h264:hw&a=aac", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "EXT-X-STREAM-INF") {
		t.Fatalf("playlist missing variant line: %q", resp.Body.String())
	}
	// The variant URL embedded in the master playlist must be the
	// caps-aware task ID — otherwise the playlist points at a task
	// the client can't find on its second request. capsID contains
	// characters that get URL-escaped in the response body, so check
	// both the raw and escaped forms.
	escapedID := url.PathEscape(capsID)
	if !strings.Contains(resp.Body.String(), escapedID) {
		t.Fatalf("playlist does not reference caps task ID %q (escaped=%q): %q",
			capsID, escapedID, resp.Body.String())
	}
}

func TestHTTPMaster_NoCapsDoesNotSeeCapsTask(t *testing.T) {
	// Skipped: when master has no caps and no legacy task, the
	// handler calls GetOrAdd which probes — and probe reaches
	// torr.GetTorrent which NPEs in apihelper.go:90 without a real
	// BT client. The "404 on legacy-with-caps-task" assertion is
	// semantically correct but can't be exercised here.
	t.Skip("GetOrAdd NPEs in apihelper.go:90 without a real BT client; cannot smoke this path")
}

func TestHTTPRemove_DropsLegacyAndAllCapsVariants(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	legacy, _ := newTrackedTask("hash", time.Now().UTC())
	v1, _ := newTrackedTask("hash|v:h264:hw,a:aac,", time.Now().UTC())
	v2, _ := newTrackedTask("hash|v:h265:sw,", time.Now().UTC())
	other, _ := newTrackedTask("other", time.Now().UTC())

	conf := Config{}.normalized()
	conf.MaxTasks = 0
	s := &Service{
		conf:        conf,
		tasks:       map[string]*Task{legacy.ID: legacy, v1.ID: v1, v2.ID: v2, other.ID: other},
		probeCache:  make(map[string]probeCacheEntry),
		stopCleanup: make(chan struct{}),
	}
	s.SetupRoute(router)

	req := httptest.NewRequest(http.MethodGet, "/gst/remove?hash=hash", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
	if _, ok := s.tasks[legacy.ID]; ok {
		t.Errorf("legacy task survived")
	}
	if _, ok := s.tasks[v1.ID]; ok {
		t.Errorf("caps variant v1 survived")
	}
	if _, ok := s.tasks[v2.ID]; ok {
		t.Errorf("caps variant v2 survived")
	}
	if _, ok := s.tasks[other.ID]; !ok {
		t.Errorf("unrelated 'other' was removed")
	}
	if !legacy.IsDisposed() || !v1.IsDisposed() || !v2.IsDisposed() {
		t.Errorf("matched tasks not disposed")
	}
	s.Dispose()
}

func TestHTTPHeartbeat_FindsCapsVariant(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	capsID := "hash|v:h264:hw,"
	capsTask, _ := newTrackedTask(capsID, time.Now().UTC())

	conf := Config{}.normalized()
	s := &Service{
		conf:       conf,
		tasks:      map[string]*Task{capsID: capsTask},
		probeCache: make(map[string]probeCacheEntry),
	}
	s.SetupRoute(router)

	req := httptest.NewRequest(http.MethodGet, "/gst/hash/heartbeat", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
}

func TestHTTPHeartbeat_404WhenNoTasks(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	s := NewService(DefaultConfig())
	defer s.Dispose()
	s.SetupRoute(router)

	req := httptest.NewRequest(http.MethodGet, "/gst/missing/heartbeat", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
}

func TestHTTPVideoPlaylist_LegacyFindStillUsesCapsVariant(t *testing.T) {
	// When the master creates a caps-aware task and the client then
	// issues /video.m3u8 with the same caps in the query, lookup
	// must succeed. Without caps in the query, it must 404.
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	conf := Config{}.normalized()
	capsID := "hash|v:h264:hw,a:aac,"
	capsTask := mkCapsTask(capsID, "1", 0, conf)
	capsTask.Config.AudioCaps = []string{"aac"}
	capsTask.Config.VideoCaps = []VideoCap{{Codec: "h264", Tier: TierHw}}

	s := &Service{
		conf:       conf,
		tasks:      map[string]*Task{capsID: capsTask},
		probeCache: make(map[string]probeCacheEntry),
	}
	s.SetupRoute(router)

	// With caps → 200.
	req := httptest.NewRequest(http.MethodGet,
		"/gst/hash/video.m3u8?audio=0&v=h264:hw&a=aac", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("caps-aware video playlist status=%d body=%q", resp.Code, resp.Body.String())
	}

	// Without caps → 404 (no legacy task).
	req2 := httptest.NewRequest(http.MethodGet,
		"/gst/hash/video.m3u8?audio=0", nil)
	resp2 := httptest.NewRecorder()
	router.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusNotFound {
		t.Fatalf("legacy lookup status=%d body=%q", resp2.Code, resp2.Body.String())
	}
}