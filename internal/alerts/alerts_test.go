package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type fakeAPI struct {
	events []map[string]interface{}
	calls  int
}

func (f *fakeAPI) GetEventList(macs []string, begin, end int64) ([]map[string]interface{}, error) {
	f.calls++
	var out []map[string]interface{}
	for _, e := range f.events {
		ts := int64(e["event_ts"].(float64))
		if ts >= begin && ts <= end {
			out = append(out, e)
		}
	}
	return out, nil
}

func media(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != mediaUserAgent || r.Header.Get("Authorization") != "" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".jpg"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xff, 0xd8, 0xff})
		case strings.Contains(r.URL.Path, "missing"):
			w.WriteHeader(404)
		default:
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte("mp4"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ev(base string, mac string, ts int64, value string, files ...map[string]interface{}) map[string]interface{} {
	fl := []interface{}{}
	for _, f := range files {
		fl = append(fl, f)
	}
	return map[string]interface{}{"event_id": mac + value + time.UnixMilli(ts).Format("150405"), "device_id": mac,
		"event_ts": float64(ts), "event_value": value, "tag_list": []interface{}{float64(101)}, "file_list": fl}
}

func file(base, path string, typ float64) map[string]interface{} {
	return map[string]interface{}{"type": typ, "url": base + "/resource/" + path + "?st=x"}
}

func TestPollStoresImageAndClip(t *testing.T) {
	srv := media(t)
	now := time.Now().UnixMilli()
	api := &fakeAPI{events: []map[string]interface{}{
		ev(srv.URL, "AABB", now-60000, "1", file(srv.URL, "AABB/a.jpg", 1)),
		ev(srv.URL, "CCDD", now-30000, "13", file(srv.URL, "CCDD/b.jpg", 1), file(srv.URL, "CCDD/missing.mp4", 2), file(srv.URL, "CCDD/b.mp4", 3)),
		ev(srv.URL, "EEFF", now-20000, "2", file(srv.URL, "EEFF/x.jpg", 1)), // unknown camera
	}}
	dir := t.TempDir()
	cams := func() []Camera {
		return []Camera{{Name: "porch", Nickname: "Porch", MAC: "aabb"}, {Name: "living", Nickname: "Living", MAC: "CCDD"}}
	}
	m := NewManager(dir, time.Minute, 0, api, cams, zerolog.Nop())
	if err := m.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	list := m.List("", 0)
	if len(list) != 2 {
		t.Fatalf("alerts = %+v", list)
	}
	if list[0].Camera != "living" || list[0].Kind != "motion" || list[0].Clip == "" || list[0].Image == "" {
		t.Errorf("newest = %+v", list[0])
	}
	if list[1].Camera != "porch" || list[1].Clip != "" || len(list[1].Tags) != 1 || list[1].Tags[0] != "person" {
		t.Errorf("older = %+v", list[1])
	}
	for _, a := range list {
		if _, err := os.Stat(filepath.Join(dir, a.Image)); err != nil {
			t.Errorf("image missing: %v", err)
		}
	}
	if got := m.List("porch", 0); len(got) != 1 {
		t.Errorf("filter = %d", len(got))
	}

	// Second poll: nothing new; reload from disk keeps the index.
	m.Poll(context.Background())
	if len(m.List("", 0)) != 2 {
		t.Error("duplicate alerts after re-poll")
	}
	m2 := NewManager(dir, time.Minute, 0, api, cams, zerolog.Nop())
	if len(m2.List("", 0)) != 2 {
		t.Error("index not reloaded")
	}
}

func TestPruneRemovesOldAlerts(t *testing.T) {
	srv := media(t)
	now := time.Now()
	api := &fakeAPI{events: []map[string]interface{}{
		ev(srv.URL, "AABB", now.Add(-3*time.Hour).UnixMilli(), "1", file(srv.URL, "AABB/old.jpg", 1)),
		ev(srv.URL, "AABB", now.Add(-time.Minute).UnixMilli(), "2", file(srv.URL, "AABB/new.jpg", 1)),
	}}
	dir := t.TempDir()
	m := NewManager(dir, time.Minute, 2*time.Hour, api, func() []Camera { return []Camera{{Name: "porch", MAC: "AABB"}} }, zerolog.Nop())
	m.Poll(context.Background())
	old := m.List("", 0)[1]
	m.prune()
	if got := m.List("", 0); len(got) != 1 || got[0].Kind != "sound" {
		t.Fatalf("after prune = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, old.Image)); !os.IsNotExist(err) {
		t.Error("old image not deleted")
	}
}

func TestLabels(t *testing.T) {
	if KindLabel(1) != "motion" || KindLabel(2) != "sound" || KindLabel(13) != "motion" || KindLabel(99) != "event 99" {
		t.Error("kind labels")
	}
	if TagLabel(101) != "person" || TagLabel(102) != "vehicle" || TagLabel(5) != "" {
		t.Error("tag labels")
	}
	if macFromURL("https://x/resource/d03f27/2026/a.jpg") != "D03F27" {
		t.Error("macFromURL")
	}
}
