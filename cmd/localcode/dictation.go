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
	"time"

	"localcode/internal/dictation"
)

const dictationUsage = `usage: localcode dictation <subcommand>

  localcode dictation install [--dir <path>] [--engine whisper|sherpa]
                       download the speech engine and its model, then report where
                       they went. Does nothing if they are already there. Defaults
                       to whisper, which works in every build; sherpa is the older
                       engine and needs a desktop build. --dir defaults to beside
                       this binary, which is where localcode looks by default.
                       Whisper is ~200MB to download; sherpa about 400MB.
  localcode dictation status
                       report whether dictation can run right now, and why not.
  localcode dictation remove [--dir <path>] [--engine whisper|sherpa]
                       delete an installed engine and model. Used by the Windows
                       uninstaller, which would otherwise leave the download behind.
  localcode dictation probe
                       ask the configured remote speech server what it actually
                       answers, endpoint by endpoint, and print every reply. Use
                       this when dictation produces nothing: it separates "the
                       address is wrong" from "HTTP works but the upload is being
                       dropped" from "this endpoint does not exist", which all
                       look the same from the prompt box.
  localcode dictation test <recording.wav>
                       transcribe a 16 kHz mono WAV with the configured model and
                       print the tokens behind the text as well as the text. Use
                       this when a transcript comes out wrong: it separates the
                       model mishearing from the text being assembled wrongly,
                       which look identical in the finished sentence. Desktop
                       build only (on Windows, localcode-gui.exe).

Dictation runs in the desktop window and the Web UI (see docs/USAGE.md).`

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
	case "test":
		return runDictationTest(args[1:])
	case "probe":
		return runDictationProbe()
	default:
		fmt.Println(dictationUsage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// installFlags parses "--dir <path>" and "--engine <name>".
//
// --dir defaults to the models directory beside this binary — the one
// the daemon looks in when nothing is configured, so an install with no
// arguments is a complete setup.
//
// The engine defaults to whisper: it is the one that works in every
// build, and someone running install with no arguments wants the setup
// that will work, not the historical one.
func installFlags(args []string) (dir string, engine dictation.Engine, err error) {
	engine = dictation.EngineWhisper
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir", "-d":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--dir needs a path")
			}
			dir = args[i+1]
			i++
		case "--engine", "-e":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--engine needs a name")
			}
			switch args[i+1] {
			case string(dictation.EngineWhisper), string(dictation.EngineSherpa):
				engine = dictation.Engine(args[i+1])
			default:
				return "", "", fmt.Errorf("unknown engine %q (want %q or %q)", args[i+1], dictation.EngineWhisper, dictation.EngineSherpa)
			}
			i++
		default:
			return "", "", fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if dir == "" {
		if dir, err = dictation.BundledModelParent(); err != nil {
			return "", "", err
		}
	}
	dir, err = filepath.Abs(dir)
	return dir, engine, err
}

func runDictationRemove(args []string) error {
	abs, engine, err := installFlags(args)
	if err != nil {
		return err
	}
	if engine == dictation.EngineSherpa {
		if err := dictation.Remove(abs); err != nil {
			return err
		}
		fmt.Printf("removed any sherpa model under %s\n", abs)
		return nil
	}
	if err := dictation.RemoveWhisper(abs); err != nil {
		return err
	}
	fmt.Printf("removed any whisper engine and model in %s\n", abs)
	return nil
}

func runDictationInstall(args []string) error {
	abs, engine, err := installFlags(args)
	if err != nil {
		return err
	}
	if engine == dictation.EngineWhisper {
		return installWhisper(abs)
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

// installWhisper fetches the engine and its model into dir.
//
// Engine and DLLs land in the same directory, which is what Windows
// requires: a program's imports are resolved from its own directory
// before any of its code runs. That directory does not have to be the
// one holding localcode.exe, since it is whisper-server.exe doing the
// loading — so the whole engine stays under models/ with everything
// else that was downloaded rather than fetched.
func installWhisper(dir string) error {
	if dictation.WhisperInstalled(dir) {
		fmt.Printf("already installed: %s\n", dir)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("installing the whisper speech engine into %s\n", dir)
	err := dictation.InstallWhisper(ctx, dir, func(stage string, done, total int64) {
		if total <= 0 {
			return
		}
		fmt.Printf("\r  %s: %d/%d MB (%d%%)   ", stage, done>>20, total>>20, done*100/total)
	})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Println("dictation is ready — no config change needed, since this is where localcode looks by default.")
	return nil
}

// runDictationProbe asks the configured remote speech server what it
// actually does, one endpoint at a time, and prints every answer.
//
// It exists because the failure it diagnoses tells you nothing on its
// own: a server that resets the connection produces "an existing
// connection was forcibly closed by the remote host" with no status and
// no body, three candidate endpoints fail identically, and the transcript
// gets one line naming only the first. Separating "does HTTP work at all"
// from "does this endpoint accept the audio" is what makes it a question
// someone can act on.
func runDictationProbe() error {
	e, err := resolveEnv()
	if err != nil {
		return err
	}
	cfg, err := loadConfig("", e)
	if err != nil {
		return err
	}
	dcfg := dictationConfig(cfg)
	if dcfg.RemoteHost() == "" {
		return fmt.Errorf("no remote speech server configured — set dictation.whisper_url in config.json")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := dictation.Probe(ctx, dcfg)
	if err != nil {
		return err
	}

	fmt.Printf("speech server: %s\n\n", res.Address)
	for _, s := range res.Steps {
		mark := "FAIL"
		if s.OK {
			mark = "ok"
		}
		fmt.Printf("  %-4s %-34s %s\n", mark, s.What, s.Status)
		if s.Detail != "" {
			fmt.Printf("       %s\n", s.Detail)
		}
	}
	fmt.Printf("\n%s\n", res.Summary())
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
	// The full config, not just the model directory: with two engines
	// to choose between, an answer derived from half the settings is an
	// answer about a setup nobody is running.
	cfg := dictation.Config{ModelDir: resolveDictationModelDir(configured)}
	if e, err := resolveEnv(); err == nil {
		if full, err := loadConfig("", e); err == nil {
			cfg = dictationConfig(full)
		}
	}
	ready, why := dictation.NewManager(cfg).Ready()
	if cfg.ModelDir != "" {
		fmt.Println("sherpa model directory:", cfg.ModelDir)
	}
	if ready {
		fmt.Println("dictation is ready")
		if d := dictation.Describe(cfg); d != "" {
			fmt.Println("engine:", d)
		}
		return nil
	}
	fmt.Println("dictation is not available:", why)
	return nil
}

// runDictationTest transcribes one recording and prints what the model
// produced, tokens included.
//
// "The transcript is wrong" covers two faults that need opposite fixes
// and are indistinguishable in the finished text: the model mishearing,
// and correct tokens being joined wrongly. Only the token list tells
// them apart — see dictation.Diagnosis.
func runDictationTest(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: localcode dictation test <recording.wav> (16 kHz mono, 16-bit)")
	}
	if !dictation.Available() {
		return fmt.Errorf("this build has no speech recognizer; on Windows run localcode-gui.exe dictation test")
	}

	samples, _, err := dictation.ReadWAV(args[0])
	if err != nil {
		return err
	}

	dir := dictation.DefaultModelDir()
	if dir == "" {
		return fmt.Errorf("no speech model found — run `localcode dictation install`, or set dictation_model_dir in config.json")
	}
	fmt.Printf("model:  %s\n", dir)

	d, err := dictation.Diagnose(dictation.Config{ModelDir: dir}, samples)
	if err != nil {
		return err
	}
	fmt.Print(d)
	return nil
}
