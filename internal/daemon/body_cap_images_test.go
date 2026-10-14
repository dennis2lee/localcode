package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/provider"
)

func postMessage(t *testing.T, url string, body map[string]any) (int, string) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// A message may carry 10 MB of images, which the page checks and the
// daemon names. It travels as base64 inside the JSON body, and the body
// was capped at 1 MB, so an image past about 768 KB, an ordinary
// screenshot, was refused as "request body too large". The text keeps the
// 1 MB cap.
func TestAMessageCarriesImagesUpToTheirOwnLimit(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/sessions/s1/messages"
	png := func(n int) []byte { return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...) }

	code, body := postMessage(t, url, map[string]any{"text": "what is this",
		"images": []map[string]any{{"media_type": "image/png", "data": png(9 << 20)}}})
	if code != http.StatusAccepted {
		t.Fatalf("a 9 MB image: status %d (%s), want 202", code, body)
	}
	waitFor(t, func() bool { return len(d.turns.anyBusy([]string{"s1"})) == 0 })

	code, body = postMessage(t, url, map[string]any{"text": "and this",
		"images": []map[string]any{{"media_type": "image/png", "data": png(provider.MaxImageBytesPerMessage)}}})
	if code != http.StatusBadRequest || !strings.Contains(body, "10MB limit") {
		t.Errorf("images over the limit: status %d (%s), want 400 naming the 10MB limit", code, body)
	}

	code, body = postMessage(t, url, map[string]any{"text": strings.Repeat("a", maxJSONBody+1)})
	if code != http.StatusBadRequest {
		t.Errorf("text over 1 MB: status %d (%s), want 400", code, body)
	}
}

func upload(t *testing.T, url, name string, size int) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(make([]byte, size))
	mw.Close()
	resp, err := http.Post(url, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// A file over the upload limit is refused, not cut to the limit and
// answered 200: the model then read a truncated file as the whole of it.
// A file at the limit is kept whole.
func TestAnUploadOverTheLimitIsRefusedNotTruncated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/sessions/s1/uploads"
	dir := filepath.Join(home, ".localcode", "uploads", "s1")

	if code, body := upload(t, url, "big.bin", maxUploadBytes+1); code != http.StatusRequestEntityTooLarge {
		t.Errorf("one byte over: status %d (%s), want 413", code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Error("a refused upload left a file behind")
	}

	code, body := upload(t, url, "exact.bin", maxUploadBytes)
	if code != http.StatusOK {
		t.Fatalf("exactly at the limit: status %d (%s), want 200", code, body)
	}
	if info, err := os.Stat(filepath.Join(dir, "exact.bin")); err != nil || info.Size() != maxUploadBytes {
		t.Errorf("the file at the limit was not kept whole: %v, %v", info, err)
	}
}

// A refused upload leaves an earlier file of the same name as it was. The
// file was opened under its own name before the copy, so a refusal
// truncated or removed the copy the conversation already pointed at, and
// nothing written on the way is left beside it.
func TestARefusedUploadKeepsTheEarlierFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/sessions/s1/uploads"
	dir := filepath.Join(home, ".localcode", "uploads", "s1")

	if code, body := upload(t, url, "keep.bin", 5); code != http.StatusOK {
		t.Fatalf("first upload: status %d (%s)", code, body)
	}
	if code, body := upload(t, url, "keep.bin", maxUploadBytes+1); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("second upload: status %d (%s), want 413", code, body)
	}
	if info, err := os.Stat(filepath.Join(dir, "keep.bin")); err != nil || info.Size() != 5 {
		t.Errorf("the earlier upload was not kept: %v, %v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("uploads dir holds %v, want only keep.bin", names)
	}
}
