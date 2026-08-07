// `localcode dictation` sets up voice input by fetching the speech model
// it needs. Dictation is off until a model is on disk, and the model is
// far too large to ship in an installer, so the choice is between asking
// everyone to follow a five-step download-and-unpack routine by hand and
// making it one command. This is that command.
//
// The Windows installer calls exactly this, which is why it lives here
// rather than in the desktop build: the installer runs `localcode.exe`,
// the console binary, and it has to work in a build with no webview.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"localcode/internal/dictation"
)

const dictationUsage = `usage: localcode dictation <subcommand>

  localcode dictation install [--dir <path>]
                       download the speech model and unpack it, then print the
                       directory. Does nothing if a usable model is already there.
                       --dir defaults to the models directory beside this binary,
                       which is where localcode looks when dictation_model_dir is
                       not set. About 400MB to download, ~130MB kept.
  localcode dictation status
                       report whether dictation can run right now, and why not.
  localcode dictation remove [--dir <path>]
                       delete an installed model. Used by the Windows uninstaller,
                       which would otherwise leave ~130MB behind.

Dictation itself only works in the desktop window (see docs/USAGE.md).`

func runDictation(args []string) error {
	if len(args) == 0 {
		fmt.Println(dictationUsage)
		return nil
	}
	switch args[0] {
	case "install":
		return runDictationInstall(args[1:])
	case "status":
		return runDictationStatus()
	case "remove":
		return runDictationRemove(args[1:])
	default:
		fmt.Println(dictationUsage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// modelParentFlag parses the shared "--dir <path>" argument, defaulting
// to the models directory beside this binary — the one the daemon looks
// in when dictation_model_dir is unset, so an install with no arguments
// is a complete setup.
func modelParentFlag(args []string) (string, error) {
	dir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir", "-d":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--dir needs a path")
			}
			dir = args[i+1]
			i++
		default:
			return "", fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if dir == "" {
		var err error
		if dir, err = dictation.BundledModelParent(); err != nil {
			return "", err
		}
	}
	return filepath.Abs(dir)
}

func runDictationRemove(args []string) error {
	abs, err := modelParentFlag(args)
	if err != nil {
		return err
	}
	if err := dictation.Remove(abs); err != nil {
		return err
	}
	fmt.Printf("removed any model under %s\n", abs)
	return nil
}

func runDictationInstall(args []string) error {
	abs, err := modelParentFlag(args)
	if err != nil {
		return err
	}

	if existing := dictation.Installed(abs); existing != "" {
		fmt.Printf("already installed: %s\n", existing)
		return nil
	}

	// Ctrl-C has to actually stop a 400MB download rather than being
	// swallowed while io.Copy runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("downloading %s\n  to %s\n", dictation.ModelURL, abs)
	installed, err := dictation.Install(ctx, abs, func(done, total int64) {
		fmt.Printf("\r  %d/%d MB (%d%%)   ", done>>20, total>>20, done*100/total)
	})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("installed: %s\n", installed)
	fmt.Println("dictation is ready — no config change needed, since this is where localcode looks by default.")
	return nil
}

func runDictationStatus() error {
	// A missing or unfinished config is not an error here. Whether
	// dictation can run has nothing to do with which model providers are
	// set up, and refusing to answer until they are would make this
	// useless at exactly the moment it is most wanted — right after
	// installing, before anything else is configured.
	configured := ""
	if e, err := resolveEnv(); err == nil {
		if cfg, err := loadConfig("", e); err == nil {
			configured = cfg.DictationModelDir
		}
	}
	dir := resolveDictationModelDir(configured)
	ready, why := dictation.NewManager(dictation.Config{ModelDir: dir}).Ready()
	if dir != "" {
		fmt.Println("model directory:", dir)
	}
	if ready {
		fmt.Println("dictation is ready")
		return nil
	}
	fmt.Println("dictation is not available:", why)
	return nil
}
