package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"localcode/internal/config"
	"localcode/internal/dictation"
)

// maxAudioChunkBytes bounds one POST of audio. At 16kHz 16-bit mono a
// second is 32KB, so this is about eight seconds — far more than the
// quarter-second chunks a client actually sends, and small enough that a
// confused (or hostile) caller cannot make the daemon buffer megabytes
// per request.
const maxAudioChunkBytes = 256 << 10

// handleDictationStatus tells a client whether to offer a microphone
// button at all, and why not when it shouldn't. A button that can only
// fail is worse than no button, and "it didn't work" with no reason is
// worse than either — hence the detail.
func (d *Daemon) handleDictationStatus(w http.ResponseWriter, r *http.Request) {
	if d.Dictation == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ready":  false,
			"detail": dictation.ErrUnavailable.Error(),
		})
		return
	}
	ready, detail := d.Dictation.Ready()
	cfg := d.Dictation.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"ready":  ready,
		"detail": detail,
		// The current settings travel with the status so a settings panel
		// can show what is in force without a second request, and so the
		// engine's identity is visible: whether audio is staying on this
		// machine is not something to have to go and look up.
		"language":    cfg.Language,
		"whisper_url": cfg.WhisperURL,
		"whisper_api": cfg.WhisperAPI,
		"engine":      dictation.Describe(cfg),
		"remote":      cfg.RemoteHost() != "",
		// Whether the daemon has a config.json to write to. Without one
		// the panel can still change the live setting, but it will not
		// survive a restart, and saying so beats a control that silently
		// forgets.
		"can_save": d.ConfigPath != "",
	})
}

// handleSetDictation changes the speech settings a settings panel owns —
// the spoken language and, when dictation should run somewhere else, the
// address of that engine.
//
// Applied to the live manager first and persisted second, in that order
// deliberately: the setting taking effect is what the user asked for, and
// a daemon started without a config.json can still be configured for as
// long as it runs. A failed write is reported rather than swallowed, but
// it does not undo the change.
func (d *Daemon) handleSetDictation(w http.ResponseWriter, r *http.Request) {
	if d.Dictation == nil {
		writeError(w, http.StatusServiceUnavailable, dictation.ErrUnavailable)
		return
	}
	var req struct {
		Language   string `json:"language"`
		WhisperURL string `json:"whisper_url"`
		// WhisperAPI names the dialect a remote server speaks, or "" to
		// work it out on the first utterance. It is in the panel because
		// the discovery costs one refused request per candidate on a
		// server that answers oddly, and because a server that answers
		// every path with a 200 cannot be discovered at all.
		WhisperAPI string `json:"whisper_api"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	cfg := d.Dictation.Config()
	cfg.Language = strings.TrimSpace(req.Language)
	cfg.WhisperURL = strings.TrimSpace(req.WhisperURL)
	cfg.WhisperAPI = strings.TrimSpace(req.WhisperAPI)
	// Rejected here rather than at the first attempt to dictate: a
	// settings panel is where someone can still see what they typed.
	if cfg.WhisperURL != "" && cfg.RemoteHost() == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not an address a speech engine could be at", req.WhisperURL))
		return
	}
	if cfg.WhisperAPI != "" && !dictation.KnownAPI(cfg.WhisperAPI) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a speech API localcode knows (%s)", cfg.WhisperAPI, dictation.APINames()))
		return
	}
	d.Dictation.SetConfig(cfg)

	var saveErr string
	if d.ConfigPath != "" {
		if err := config.SetDictationInFile(d.ConfigPath, cfg.Language, cfg.WhisperURL, cfg.WhisperAPI); err != nil {
			saveErr = err.Error()
		}
	}
	ready, detail := d.Dictation.Ready()
	writeJSON(w, http.StatusOK, map[string]any{
		"ready":       ready,
		"detail":      detail,
		"language":    cfg.Language,
		"whisper_url": cfg.WhisperURL,
		"whisper_api": cfg.WhisperAPI,
		"engine":      dictation.Describe(cfg),
		"remote":      cfg.RemoteHost() != "",
		"save_error":  saveErr,
	})
}

// handleDictationStart opens a recognizer and returns the id to post
// audio to.
func (d *Daemon) handleDictationStart(w http.ResponseWriter, r *http.Request) {
	if d.Dictation == nil {
		writeError(w, http.StatusNotImplemented, dictation.ErrUnavailable)
		return
	}
	id, err := d.Dictation.Start()
	if err != nil {
		// 503, not 500: the daemon is fine, the model isn't there. The
		// message says which, and it is shown to the user verbatim.
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// handleDictationAudio takes one chunk of 16-bit little-endian PCM at
// dictation.SampleRate and answers with the text so far.
//
// Plain request/response, one chunk at a time, rather than a WebSocket:
// the client already drives the cadence (it decides when a chunk is
// full), the reply is small, and this needs no new dependency and no
// second protocol to keep alive, reconnect and test.
func (d *Daemon) handleDictationAudio(w http.ResponseWriter, r *http.Request) {
	if d.Dictation == nil {
		writeError(w, http.StatusNotImplemented, dictation.ErrUnavailable)
		return
	}
	sess, err := d.Dictation.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	pcm, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAudioChunkBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read audio: %w", err))
		return
	}

	res, err := sess.Write(r.Context(), pcm)
	if err != nil {
		// 404 rather than 400, because the only way to get here is a
		// session that has stopped — usually reaped while the browser
		// was still uploading. The client already knows how to start a
		// new one when a session is gone; a 400 told it the audio was
		// malformed, which it was not, and it gave up instead.
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDictationStop ends a session and returns whatever was still in
// progress, so a sentence cut off by clicking the microphone off is not
// silently thrown away.
func (d *Daemon) handleDictationStop(w http.ResponseWriter, r *http.Request) {
	if d.Dictation == nil {
		writeError(w, http.StatusNotImplemented, dictation.ErrUnavailable)
		return
	}
	res, err := d.Dictation.Stop(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
