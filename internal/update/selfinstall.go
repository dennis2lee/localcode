package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"localcode/internal/childproc"
)

// maxBinary caps what will be unpacked out of a release archive. The
// download was checked against GitHub's SHA-256 before it got here, so
// this is not about a hostile file; it is about not filling a home
// directory with a decompression bomb if the archive is ever wrong.
const maxBinary = 512 << 20

// selfInstall replaces exe with the localcode binary inside archive.
//
// This is what makes a root-free install a complete one. Somebody who
// unpacked localcode into ~/.local/bin owns that file, so replacing it
// needs no package manager, no password, and no second copy on the
// machine — and the one thing that would leave a half-installed program
// behind, writing into a file another process is executing, is exactly
// what rename avoids: the running localcode keeps the inode it started
// from and the new binary takes the name.
//
// The new binary is asked for its version before it takes the name. An
// archive built for the wrong architecture unpacks perfectly and then
// fails to execute, and finding that out after the rename means the
// person is left with an install that does not start.
func selfInstall(archive, exe string) error {
	if runtime.GOOS == "windows" {
		// Windows holds an open image lock on the running .exe, and this
		// platform has an MSI that does the job properly anyway.
		return errors.New("Windows installs come from the MSI")
	}
	if strings.Contains(filepath.ToSlash(exe), ".app/Contents/MacOS/") {
		// A .app is a directory with a signature over the whole of it.
		// Swapping the binary inside one leaves a bundle whose Info.plist
		// and resources belong to the old version.
		return errors.New("this copy is inside LocalCode.app, which is replaced as a bundle")
	}

	// The archive is opened before anything is written: a file that turns
	// out not to be a localcode tarball should leave the install
	// directory exactly as it was, not a stray temp file behind.
	src, done, err := openBinary(archive)
	if err != nil {
		return err
	}
	defer done()

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".localcode-update-*")
	if err != nil {
		// The usual reason: localcode lives somewhere only root can
		// write, which is the case the .deb covers.
		return fmt.Errorf("%s is not writable by this user: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := runsAtAll(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("could not put the new binary in place: %w", err)
	}
	return nil
}

// openBinary finds the localcode binary inside a release tarball and
// returns a reader positioned at it, along with the close to run when the
// caller is done with it.
//
// Only a plain file called "localcode" at the root of the archive counts,
// which is also how the bare-binary tarball is told apart from the macOS
// .app one: every entry in that one starts with "LocalCode.app/".
func openBinary(archive string) (io.Reader, func(), error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a gzip archive: %w", filepath.Base(archive), err)
	}
	done := func() { gz.Close(); f.Close() }

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			done()
			return nil, nil, err
		}
		if h.Typeflag != tar.TypeReg || filepath.Clean(h.Name) != "localcode" {
			continue
		}
		if h.Size > maxBinary {
			done()
			return nil, nil, fmt.Errorf("the binary in %s is %d bytes, which is not a localcode", filepath.Base(archive), h.Size)
		}
		return io.LimitReader(tr, maxBinary), done, nil
	}
	done()
	return nil, nil, fmt.Errorf("%s contains no localcode binary", filepath.Base(archive))
}

// runsAtAll checks that the downloaded binary starts on this machine.
func runsAtAll(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	childproc.Hide(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("the downloaded localcode does not run here (%v): %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return errors.New("the downloaded localcode printed no version")
	}
	return nil
}

// currentBinary is the file this process is running from, with symlinks
// resolved: installing over the symlink would replace the link with a
// binary and leave the real file behind, still on PATH somewhere.
func currentBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}
