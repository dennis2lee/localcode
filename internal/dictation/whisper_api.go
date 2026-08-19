package dictation

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// This file is about the fact that "a whisper server" is not one protocol.
//
// localcode spawns whisper.cpp's own server locally, so for years the only
// endpoint that existed was whisper.cpp's: POST /inference, the audio in a
// form field called "file", the answer in {"text": ...}. Pointing
// whisper_url at a server on another machine kept using exactly that, and
// a remote server is precisely where it is likely to be something else.
//
// The report that prompted this was a WhisperX ASR server: it serves
// /asr and /v1/audio/transcriptions, and answers /inference with 404. So
// dictation ran, sent audio every 900ms, got 404 every time, and said
// nothing at all — the microphone was on and no text ever appeared.
//
// Rather than requiring the server to be changed, or a translating proxy
// in front of it, localcode learns the dialects. There are three worth
// knowing and they differ in three small ways each: the path, the name of
// the file field, and which extra fields are accepted.

// whisperAPI is one server dialect.
type whisperAPI struct {
	// name is what a diagnostic prints.
	name string
	// path is the endpoint, rooted at the server.
	path string
	// fileField is the multipart field the audio goes in. This is the
	// difference that produces the least helpful error of the three: a
	// server that wants "audio_file" answers a request carrying "file"
	// with a validation error about a missing field, not with anything
	// that says which field it wanted.
	fileField string
	// extra are form fields to send beyond the audio and the language.
	// Empty for servers that reject unknown fields.
	extra map[string]string
	// query are URL query parameters.
	//
	// They exist for one server that does not take its options where the
	// others do: the whisperX ASR service reads everything except the
	// audio from the query string and ignores unknown form fields
	// entirely. Sent as form fields, "output=json" and the spoken
	// language were dropped without a word — so that server auto-detected
	// every utterance no matter what the settings panel said, which is
	// exactly "I chose English and it still comes back in Korean".
	query map[string]string
	// languageInQuery sends the spoken language as a query parameter
	// rather than a form field, for the same reason.
	languageInQuery bool
}

// whisperAPIs are tried in this order when the dialect is not configured.
//
// OpenAI-compatible first: it is what most servers put in front of a
// model these days, including WhisperX's, and a server that speaks it is
// the one least likely to be surprised by the request. whisper.cpp's own
// /inference second, since that is what a locally spawned engine serves
// and the overwhelmingly common local case. WhisperX's native /asr last,
// as the fallback for a server that offers only that.
var whisperAPIs = []whisperAPI{
	{
		name:      "openai",
		path:      "/v1/audio/transcriptions",
		fileField: "file",
		// No temperature: some implementations reject it outright, and
		// dictation has no use for a non-zero one anyway. "model" is
		// omitted for the same reason — a server hosting one model
		// rejects a name it does not recognise, and one hosting several
		// has already been told which to load.
		extra: map[string]string{"response_format": "json"},
	},
	{
		name:      "whispercpp",
		path:      "/inference",
		fileField: "file",
		// temperature_inc is the decoding fallback, and it is here so that
		// a remote engine decodes the way the local one does.
		//
		// localcode starts its own engine, so its command line is ours to
		// choose; a server on another machine was started by someone else,
		// with whatever options they picked, and `--no-fallback` there
		// reproduces exactly the fault v0.43.0 fixed locally — a segment
		// whose decode looks unconfident is dropped instead of retried,
		// which is what fast speech looks like from the inside. Sent per
		// request, it overrides that: 0.2 is whisper.cpp's own default
		// increment, so this asks for the standard behaviour rather than
		// imposing an opinion.
		extra: map[string]string{"response_format": "json", "temperature": "0.0", "temperature_inc": "0.2"},
	},
	{
		name:      "whisperx",
		path:      "/asr",
		fileField: "audio_file",
		// "output", not "output_format", and in the query rather than the
		// form: this service's default output is plain text, which
		// parseTranscript can only report as unreadable JSON. task is sent
		// explicitly so a server configured to translate by default still
		// transcribes — dictation is typing what someone said, never a
		// translation of it.
		query:           map[string]string{"output": "json", "task": "transcribe", "encode": "true"},
		languageInQuery: true,
	},
}

// whisperCppAPI is the dialect a locally spawned engine speaks. It is
// whisper.cpp because startWhisper is what started it.
func whisperCppAPI() whisperAPI {
	api, _ := apiByName("whispercpp")
	return api
}

// apiByName finds a dialect the user named in config, so a server that
// would otherwise be probed for can be stated outright.
func apiByName(name string) (whisperAPI, bool) {
	for _, a := range whisperAPIs {
		if strings.EqualFold(a.name, name) {
			return a, true
		}
	}
	return whisperAPI{}, false
}

// KnownAPI reports whether name is a dialect this package can be told to
// use, so a settings panel can refuse a value at the point it is typed
// rather than at the first utterance.
func KnownAPI(name string) bool {
	_, ok := apiByName(name)
	return ok
}

// APINames lists the dialects, for a picker or an error message.
func APINames() string { return whisperAPINames() }

// whisperAPINames lists what whisper_api accepts, for the error message
// when it is set to something else.
func whisperAPINames() string {
	names := make([]string, len(whisperAPIs))
	for i, a := range whisperAPIs {
		names[i] = a.name
	}
	return strings.Join(names, ", ")
}

// parseTranscript pulls the text out of whichever shape came back.
//
// Every one of these servers answers with JSON containing the words, and
// every one of them uses a different key for it — "text" for whisper.cpp
// and the OpenAI shape, "transcription" for some proxies, and a list of
// segments for a server asked for a verbose format. An error is reported
// under two different keys again.
//
// Reading all of them costs a few lines here and saves the alternative,
// which is a transcript that silently comes back empty because the words
// were under a key nobody looked at.
func parseTranscript(raw []byte) (string, error) {
	var out struct {
		Text          string `json:"text"`
		Transcription string `json:"transcription"`
		Error         string `json:"error"`
		Detail        string `json:"detail"`
		Message       string `json:"message"`
		Segments      []struct {
			Text string `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("whisper engine returned unreadable JSON: %s", truncateForError(string(raw)))
	}
	for _, msg := range []string{out.Error, out.Detail, out.Message} {
		if msg != "" {
			return "", fmt.Errorf("whisper engine: %s", msg)
		}
	}
	if out.Text != "" {
		return out.Text, nil
	}
	if out.Transcription != "" {
		return out.Transcription, nil
	}
	if len(out.Segments) > 0 {
		var b strings.Builder
		for _, seg := range out.Segments {
			b.WriteString(seg.Text)
		}
		return b.String(), nil
	}
	// Genuinely nothing said, which is normal: a window of silence
	// transcribes to an empty string rather than to an error.
	return "", nil
}

// truncateForError keeps a server's reply short enough to belong in an
// error message. An HTML 404 page is several kilobytes of nothing useful.
func truncateForError(s string) string {
	s = strings.TrimSpace(s)
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// debugf prints one line to stderr when LC_DICTATION_DEBUG is set.
//
// Which dialect a server turned out to speak is exactly the fact someone
// debugging silent dictation needs and cannot otherwise get — and it is
// also one line per engine, not per utterance, so it stays behind the
// same switch as the rest rather than becoming noise.
func debugf(format string, args ...any) {
	if os.Getenv("LC_DICTATION_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
