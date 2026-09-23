package sharing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

type nopPub struct{}

func (nopPub) Publish(string, any) {}

// fakeConns serves one rclone Connection per provider, with the provider as ID.
type fakeConns struct{}

func (fakeConns) Get(id string) (store.Connection, error) {
	return store.Connection{ID: id, Name: id, Kind: "remote", Provider: id, RcloneRemote: id}, nil
}

func (fakeConns) Config(store.Connection) (map[string]string, error) { return map[string]string{}, nil }

// rcCall is one recorded rclone rc call.
type rcCall struct {
	method string
	in     map[string]any
}

func newService(t *testing.T) (*Service, *[]rcCall) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, provider := range []string{"drive", "onedrive", "yandex"} {
		c, _ := fakeConns{}.Get(provider)
		if err := st.InsertConnection(c); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st, activity.New(st, nopPub{}), fakeConns{})
	s.Now = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local) }
	calls := &[]rcCall{}
	s.RC = func(method string, in any) (map[string]any, error) {
		*calls = append(*calls, rcCall{method, in.(map[string]any)})
		return map[string]any{"url": "https://example.com/s/abc"}, nil
	}
	return s, calls
}

func TestRcloneLinkExpiryOnlyWhereSupported(t *testing.T) {
	for _, tc := range []struct {
		provider string
		expires  bool
	}{{"drive", false}, {"onedrive", true}} {
		s, calls := newService(t)
		sh, err := s.Create(context.Background(), CreateParams{ConnectionID: tc.provider, Path: "a.wav", Kind: "publicLink", ExpireDate: "2026-09-30"})
		if err != nil {
			t.Fatal(err)
		}
		_, passed := (*calls)[0].in["expire"]
		if passed != tc.expires || (sh.ExpireDate != nil) != tc.expires {
			t.Errorf("%s: expire passed %v, stored %v; want %v", tc.provider, passed, sh.ExpireDate, tc.expires)
		}
	}
}

func TestDeleteRcloneLinkReportsStillActive(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		rcErr          error
		unlinkCalled   bool
		stillActive    bool
	}{
		{"unsupported backend", "drive", nil, false, true},
		{"unlinked", "yandex", nil, true, false},
		{"unlink failed", "yandex", errors.New("boom"), true, true},
	} {
		s, calls := newService(t)
		sh, err := s.Create(context.Background(), CreateParams{ConnectionID: tc.provider, Path: "a.wav", Kind: "publicLink"})
		if err != nil {
			t.Fatal(err)
		}
		*calls = nil
		fail := tc.rcErr
		s.RC = func(method string, in any) (map[string]any, error) {
			*calls = append(*calls, rcCall{method, in.(map[string]any)})
			return nil, fail
		}
		res, err := s.Delete(context.Background(), tc.provider, sh.ID)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.RemoteStillActive != tc.stillActive {
			t.Errorf("%s: remoteStillActive = %v, want %v", tc.name, res.RemoteStillActive, tc.stillActive)
		}
		if called := len(*calls) == 1 && (*calls)[0].in["unlink"] == true; called != tc.unlinkCalled {
			t.Errorf("%s: unlink called = %v, want %v", tc.name, called, tc.unlinkCalled)
		}
		if links, _ := s.st.Links(tc.provider, nil); len(links) != 0 {
			t.Errorf("%s: registry still has %d links", tc.name, len(links))
		}
	}
}
