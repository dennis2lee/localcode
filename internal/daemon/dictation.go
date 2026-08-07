package daemon

import (
	"fmt"
	"io"
	"net/http"

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
	writeJSON(w, http.StatusOK, map[string]any{"ready": ready, "detail": detail})
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

	res, err := sess.Write(pcm)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
