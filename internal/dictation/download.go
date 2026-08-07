package dictation

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The speech model localcode installs when asked to fetch one. There is
// exactly one rather than a catalogue on purpose: the point of this is to
// turn a documented five-step setup into a single command, and a choice
// to make is a step to take. Anyone who wants a different model still
// points dictation_model_dir at it by hand.
const (
	// ModelName is both the archive's top-level directory and the name of
	// the directory this leaves behind.
	ModelName = "sherpa-onnx-streaming-zipformer-korean-2024-06-16"

	ModelURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" +
		ModelName + ".tar.bz2"

	// ModelSHA256 pins the archive.
	//
	// GitHub publishes no digest for this asset, so this was computed
	// from the downloaded file and committed. HTTPS already proves the
	// bytes came from github.com; this additionally proves they are the
	// same bytes that were reviewed, which matters because the MSI
	// unpacks this archive as an elevated process and an upstream
	// re-upload under the same tag would otherwise go unnoticed.
	ModelSHA256 = "e346a5882a409650472be17326237e24df7bf409db6b4a8a52e1a61422bf2500"

	// ModelSize is the archive's length, used only to render progress.
	ModelSize = 418218652
)

// Installed reports the path of an already-usable model under parent, or
// "" if there is not one. Install uses it to make a repeated call cheap
// rather than a second 400MB download, which matters because the Windows
// installer runs Install on every upgrade.
func Installed(parent string) string {
	dir := filepath.Join(parent, ModelName)
	if _, err := resolveModel(dir); err != nil {
		return ""
	}
	return dir
}

// Remove deletes an installed model, and only that: it joins ModelName
// onto parent itself rather than taking a directory to delete, so a
// caller that passes the wrong parent removes nothing instead of
// something else. The Windows uninstaller calls this, elevated, with a
// path built from a property — which is exactly the situation where
// "delete whatever you were handed" is the wrong shape of API.
func Remove(parent string) error {
	dir := filepath.Join(parent, ModelName)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}

// Install downloads the model archive into parent and unpacks it,
// returning the directory to point dictation_model_dir at.
//
// progress is called with bytes downloaded so far and the expected
// total. It may be nil.
func Install(ctx context.Context, parent string, progress func(done, total int64)) (string, error) {
	return install(ctx, ModelURL, ModelSHA256, parent, progress)
}

// install is Install with the source spelled out, so a test can point it
// at a small archive served locally instead of at 400MB over the network.
func install(ctx context.Context, url, wantSum, parent string, progress func(done, total int64)) (string, error) {
	if dir := Installed(parent); dir != "" {
		return dir, nil
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", parent, err)
	}

	// Downloaded to a file rather than streamed straight into the
	// extractor: the checksum has to be checked over the whole archive
	// before anything is written from it, and a stream can only be
	// verified after it has already been unpacked.
	archive, err := download(ctx, url, wantSum, parent, progress)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)

	// Unpacked beside the destination and renamed into place, so an
	// interrupted extraction leaves no half-populated model directory
	// for the next run to find and believe.
	staging, err := os.MkdirTemp(parent, ".unpack-*")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := extract(archive, staging); err != nil {
		return "", err
	}

	unpacked := filepath.Join(staging, ModelName)
	if _, err := resolveModel(unpacked); err != nil {
		return "", fmt.Errorf("the downloaded archive is not the model it should be: %w", err)
	}
	// The archive ships every model twice, float32 and int8. resolveModel
	// prefers int8, so the float32 copies are ~300MB that would sit on
	// disk unread forever.
	if err := dropUnusedWeights(unpacked); err != nil {
		return "", err
	}

	dir := filepath.Join(parent, ModelName)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear %s: %w", dir, err)
	}
	if err := os.Rename(unpacked, dir); err != nil {
		return "", fmt.Errorf("move model into place: %w", err)
	}
	return dir, nil
}

func download(ctx context.Context, url, wantSum, parent string, progress func(done, total int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// No default timeout on the client: this is a 400MB transfer and any
	// fixed deadline would be either uselessly long or wrong for a slow
	// connection. Cancellation is the caller's, through ctx.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = ModelSize
	}

	f, err := os.CreateTemp(parent, ".download-*")
	if err != nil {
		return "", fmt.Errorf("create download file: %w", err)
	}
	name := f.Name()
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, sum), &reporter{r: resp.Body, total: total, report: progress})
	closeErr := f.Close()
	if err != nil {
		os.Remove(name)
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if closeErr != nil {
		os.Remove(name)
		return "", closeErr
	}

	if got := hex.EncodeToString(sum.Sum(nil)); wantSum != "" && got != wantSum {
		os.Remove(name)
		return "", fmt.Errorf("the downloaded archive does not match the expected checksum\n  expected %s\n  got      %s", wantSum, got)
	}
	return name, nil
}

// reporter counts bytes on their way through and reports them, at most
// once a second — a progress line that repaints thousands of times a
// second is unreadable and, in a log, enormous.
type reporter struct {
	r      io.Reader
	total  int64
	done   int64
	last   time.Time
	report func(done, total int64)
}

func (p *reporter) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.report == nil {
		return n, err
	}
	now := time.Now()
	if err != nil || p.last.IsZero() || now.Sub(p.last) >= time.Second {
		p.last = now
		p.report(p.done, p.total)
	}
	return n, err
}

func extract(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	return extractTar(tar.NewReader(bzip2.NewReader(f)), dest)
}

// extractTar is the archive loop with the decompression peeled off, so a
// test can hand it a plain tar. The stdlib reads bzip2 but cannot write
// it, and the entry handling — which is the part with a security
// property to check — sits above the compression layer anyway.
func extractTar(tr *tar.Reader, dest string) error {
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		target, err := safeJoin(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			// No io.Copy size limit: the entry sizes are the archive's
			// own and the archive is checksum-pinned, so there is no
			// unbounded-input case left to defend against here.
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, devices and hard links are silently skipped
			// rather than recreated. A model is four data files and this
			// runs elevated from the Windows installer; there is no entry
			// type beyond a plain file that this needs to honour, and
			// every one of them is a way out of the destination
			// directory.
		}
	}
}

// safeJoin refuses any archive entry that would land outside dest.
//
// A tar entry names its own path, and nothing stops that path being
// "../../windows/system32/..." — the classic archive traversal. This is
// not hypothetical defensive coding here: the Windows installer runs the
// extraction as SYSTEM.
func safeJoin(dest, name string) (string, error) {
	// Archive paths are slash-separated regardless of the OS reading them.
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("refusing archive entry outside the destination: %q", name)
	}
	target := filepath.Join(dest, clean)
	// Belt and braces: compare the joined result too, which also catches
	// anything Clean did not normalise the way this expects.
	if rel, err := filepath.Rel(dest, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing archive entry outside the destination: %q", name)
	}
	return target, nil
}

// dropUnusedWeights removes the float32 models, keeping the int8 ones
// resolveModel prefers. Missing files are not an error: a future archive
// that ships only one precision should install fine.
func dropUnusedWeights(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".onnx") || strings.HasSuffix(n, ".int8.onnx") {
			continue
		}
		// Only drop a float32 file whose int8 twin is actually present,
		// or this would delete the only copy of a weight.
		int8Twin := strings.TrimSuffix(n, ".onnx") + ".int8.onnx"
		if _, err := os.Stat(filepath.Join(dir, int8Twin)); err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}
