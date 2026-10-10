package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/provider"
)

// The first bytes of each format, which is all DetectContentType reads.
var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 64)...)
	jpegBytes = append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{2}, 64)...)
)

func viewImage(t *testing.T, ctx context.Context, path string) Result {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"path": path})
	return ViewImage{}.Execute(ctx, input)
}

func TestViewImageAttachesTheFileAsWhatItIs(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, file string
		data       []byte
		mediaType  string
	}{
		{"a PNG", "plot.png", pngBytes, "image/png"},
		{"a JPEG", "photo.jpg", jpegBytes, "image/jpeg"},
		// Named by its bytes, not its extension.
		{"a JPEG saved as .png", "screenshot.png", jpegBytes, "image/jpeg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.file)
			if err := os.WriteFile(path, c.data, 0o644); err != nil {
				t.Fatal(err)
			}
			res := viewImage(t, context.Background(), path)
			if res.IsError {
				t.Fatalf("refused: %s", res.Content)
			}
			if len(res.Images) != 1 {
				t.Fatalf("%d images, want 1", len(res.Images))
			}
			img := res.Images[0]
			if img.Type != provider.BlockImage || img.MediaType != c.mediaType || !bytes.Equal(img.Data, c.data) {
				t.Errorf("image = %s %d bytes, want %s %d bytes", img.MediaType, len(img.Data), c.mediaType, len(c.data))
			}
			if !strings.Contains(res.Content, c.mediaType) {
				t.Errorf("the result does not say what was attached: %q", res.Content)
			}
		})
	}
}

// A relative path means the session's own directory, as it does for
// read_file.
func TestViewImageResolvesARelativePathInTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "doc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc", "fig.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	res := viewImage(t, WithWorkingDir(context.Background(), dir), filepath.Join("doc", "fig.png"))
	if res.IsError || len(res.Images) != 1 {
		t.Fatalf("a relative path was not found in the workspace: %s", res.Content)
	}
}

// Everything that is not an image the three wires can carry is refused with
// what to do instead, and attaches nothing.
func TestViewImageRefusesWhatItCannotShow(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("plain text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(big, append(pngBytes, make([]byte, MaxViewImageBytes)...), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, want string
	}{
		{"text", text, "text/plain"},
		{"too large", big, "over the"},
		{"a directory", dir, "directory"},
		{"missing", filepath.Join(dir, "nowhere.png"), "nowhere.png"},
		{"no path", "", "path is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := viewImage(t, context.Background(), c.path)
			if !res.IsError || len(res.Images) != 0 {
				t.Fatalf("not refused: error %v, %d images, %q", res.IsError, len(res.Images), res.Content)
			}
			if !strings.Contains(res.Content, c.want) {
				t.Errorf("the refusal does not say %q: %q", c.want, res.Content)
			}
		})
	}
}

// Reading a file, and matchable by a rule on its path, as read_file is.
func TestViewImageIsAReadOfAPath(t *testing.T) {
	if (ViewImage{}).OutsideClass() != OutsideRead {
		t.Error("view_image does not count as a read for the workspace boundary")
	}
	if got := (ViewImage{}).Subject(json.RawMessage(`{"path":"/home/me/.ssh/id_rsa"}`)); got != "/home/me/.ssh/id_rsa" {
		t.Errorf("Subject = %q, want the path", got)
	}
	if (ViewImage{}).RequiresPermission(nil) {
		t.Error("viewing an image asks where reading a file does not")
	}
}
