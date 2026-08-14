// The settings window.
//
// Two kinds of setting live here and they are not interchangeable, which
// is why the panel says which is which rather than presenting one flat
// list:
//
//   * The microphone belongs to this browser. It cannot be anything else
//     — a deviceId is meaningless on another machine, and the daemon
//     never sees audio hardware at all, only the PCM a page sends it. It
//     is remembered in localStorage.
//   * The language and the engine address belong to the daemon. They are
//     shared by every client attached to it and persist to config.json.
//
// Adding a setting later should be adding a block to index.html and a
// line to one of the two collect/apply pairs below, not a redesign.

import {
  settingsModalEl, settingsBtn, settingsSaveBtn, settingsCloseBtn, settingsSaveNoteEl,
  micDeviceSelect, dictationLanguageSelect, whisperURLInput, whisperPortInput,
  whisperAPISelect, dictationEngineNoteEl,
} from './dom.js';
import { Modal } from './modal.js';
import * as apiClient from './api.js';

export const settings = new Modal(settingsModalEl);

// MIC_DEVICE_KEY is read by dictation.js when it opens the microphone.
// A plain string in localStorage rather than app state because it has to
// survive a reload and never travels to the daemon.
export const MIC_DEVICE_KEY = 'localcode.micDeviceId';

export function selectedMicDeviceId() {
  try {
    return localStorage.getItem(MIC_DEVICE_KEY) || '';
  } catch {
    // Private-browsing modes and some webviews throw on localStorage
    // rather than returning null. The default device is a perfectly good
    // answer, so this is not worth surfacing.
    return '';
  }
}

function rememberMicDevice(id) {
  try {
    if (id) localStorage.setItem(MIC_DEVICE_KEY, id);
    else localStorage.removeItem(MIC_DEVICE_KEY);
  } catch {
    // As above: the choice applies to this session and is simply not
    // remembered. Saying so would be noise for a case nobody can act on.
  }
}

// listMicrophones fills the device dropdown.
//
// Browsers hide device labels until the page has been granted microphone
// access at least once, so before that the list is a set of anonymous
// entries. Rather than showing "Microphone 1 / Microphone 2 / …", which
// tells the user nothing they can choose between, that state is named.
async function listMicrophones() {
  const previous = selectedMicDeviceId();
  micDeviceSelect.innerHTML = '';

  const fallback = document.createElement('option');
  fallback.value = '';
  fallback.textContent = 'System default';
  micDeviceSelect.appendChild(fallback);

  if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) {
    fallback.textContent = 'System default (this browser cannot list devices)';
    return;
  }

  let devices = [];
  try {
    devices = await navigator.mediaDevices.enumerateDevices();
  } catch {
    fallback.textContent = 'System default (device list unavailable)';
    return;
  }

  const mics = devices.filter(d => d.kind === 'audioinput');
  const unnamed = mics.length > 0 && mics.every(d => !d.label);
  for (const d of mics) {
    const opt = document.createElement('option');
    opt.value = d.deviceId;
    // textContent, not innerHTML: a device label is a string from the OS
    // and there is no reason for it to be parsed as markup.
    opt.textContent = d.label || `Microphone ${micDeviceSelect.options.length}`;
    micDeviceSelect.appendChild(opt);
  }
  if (unnamed) {
    fallback.textContent = 'System default (names appear after you allow microphone access once)';
  }

  // Keep the saved choice selected even if the device is currently
  // unplugged — coming back to it should not silently reset to default.
  micDeviceSelect.value = previous;
  if (micDeviceSelect.value !== previous) {
    const missing = document.createElement('option');
    missing.value = previous;
    missing.textContent = 'Saved microphone (not connected)';
    micDeviceSelect.appendChild(missing);
    micDeviceSelect.value = previous;
  }
}

// The speech engine's address is one string in config.json ("host:port")
// and two boxes in the panel, because that is how people hold it: the
// machine is one decision and the port is another, and a single field
// invites "192.168.1.50" with no port and no hint that one was wanted.
//
// splitAddress and joinAddress are the pair that keeps the two views in
// step. They are deliberately forgiving in the same way Config.RemoteHost
// is on the daemon side: a scheme is optional, a port typed into the
// address box is honoured, and a trailing slash is nobody's mistake worth
// an error message.

export function splitAddress(url) {
  const raw = (url || '').trim().replace(/\/+$/, '');
  if (!raw) return { host: '', port: '' };
  const scheme = raw.match(/^[a-z][a-z0-9+.-]*:\/\//i);
  const rest = scheme ? raw.slice(scheme[0].length) : raw;
  // Only a trailing :digits is a port. An IPv6 literal is full of colons
  // and none of them are.
  const m = rest.match(/^(.*?):(\d+)$/);
  const host = (scheme ? scheme[0] : '') + (m ? m[1] : rest);
  return { host, port: m ? m[2] : '' };
}

export function joinAddress(host, port) {
  const h = (host || '').trim().replace(/\/+$/, '');
  const p = (port || '').trim();
  if (!h) return ''; // no address at all means "run it on this machine"
  // A port typed into the address box wins over an empty port box, so
  // pasting "box:9000" into one field still works.
  if (!p) return h;
  return `${h.replace(/:\d+$/, '')}:${p}`;
}

// applyDictationStatus puts the daemon's answer on screen. Shared by the
// open path and the save path so both show the same thing.
function applyDictationStatus(status) {
  dictationLanguageSelect.value = status.language || '';
  const { host, port } = splitAddress(status.whisper_url);
  whisperURLInput.value = host;
  whisperPortInput.value = port;
  whisperAPISelect.value = status.whisper_api || '';

  // Which engine is in force, said plainly — in particular whether audio
  // is leaving the machine, which is not something to have to infer from
  // whether a text box looks empty.
  let note = status.engine || '';
  if (!status.ready && status.detail) note = status.detail;
  dictationEngineNoteEl.textContent = note;
  dictationEngineNoteEl.className = status.remote ? 'note warn' : 'note';
}

export async function openSettings() {
  settingsSaveNoteEl.textContent = '';
  settings.open();
  await listMicrophones();
  try {
    const status = await apiClient.dictationStatus();
    applyDictationStatus(status);
    if (status.can_save === false) {
      settingsSaveNoteEl.textContent =
        'This daemon was started without a config.json, so changes apply until it restarts and are not saved.';
    }
  } catch (err) {
    dictationEngineNoteEl.textContent = `could not read the current settings: ${err}`;
  }
}

async function saveSettings() {
  rememberMicDevice(micDeviceSelect.value);
  settingsSaveNoteEl.textContent = 'Saving…';
  try {
    const status = await apiClient.setDictationSettings({
      language: dictationLanguageSelect.value,
      whisper_url: joinAddress(whisperURLInput.value, whisperPortInput.value),
      whisper_api: whisperAPISelect.value,
    });
    applyDictationStatus(status);
    // A failed write is reported and the change still stands: it is in
    // force for as long as the daemon runs, and pretending otherwise
    // would be a second, invented failure.
    settingsSaveNoteEl.textContent = status.save_error
      ? `Applied, but not written to config.json: ${status.save_error}`
      : 'Saved.';
  } catch (err) {
    settingsSaveNoteEl.textContent = `Not saved: ${err}`;
  }
}

export function initSettings() {
  settingsBtn.addEventListener('click', openSettings);
  settingsSaveBtn.addEventListener('click', saveSettings);
  settingsCloseBtn.addEventListener('click', () => settings.close());
}
