// web/src/prgrok-tts.js
// In-browser Kokoro text-to-speech for PRGrok's Narrate: the voice list, the
// one-time model download (about 80 MB, then cached by the browser), WAV
// synthesis, and a section-by-section player. The runtime comes from a CDN on
// first use, like Mermaid and three.js.

const PRG_TTS_MODEL = 'onnx-community/Kokoro-82M-v1.0-ONNX';
const PRG_TTS_CDN = 'https://cdn.jsdelivr.net/npm/kokoro-js@1.2.1/+esm';
const PRG_TTS_TRY = 'The quick brown fox jumps over the lazy dog';
const PRG_TTS_IDS = [
  'af_heart', 'af_alloy', 'af_aoede', 'af_bella', 'af_jessica', 'af_kore',
  'af_nicole', 'af_nova', 'af_river', 'af_sarah', 'af_sky',
  'am_adam', 'am_echo', 'am_eric', 'am_fenrir', 'am_liam', 'am_michael',
  'am_onyx', 'am_puck', 'am_santa',
  'bf_alice', 'bf_emma', 'bf_isabella', 'bf_lily',
  'bm_daniel', 'bm_fable', 'bm_george', 'bm_lewis',
];

export const PRG_VOICES = PRG_TTS_IDS.map(id => ({
  id,
  locale: id[0] === 'b' ? 'en-GB' : 'en-US',
  gender: id[1] === 'm' ? 'male' : 'female',
  name: id.split('_')[1].replace(/^./, c => c.toUpperCase()),
}));

export const prgIsVoice = v => PRG_TTS_IDS.includes(String(v || ''));

let prgTtsPromise = null;
let prgTtsOk = false;
let prgTtsPreviewAudio = null;

export const prgTtsReady = () => prgTtsOk;

/** Download (first time) and initialise the model. Safe to call repeatedly. */
export function prgTtsLoad(onProgress) {
  if (!prgTtsPromise) {
    prgTtsPromise = (async () => {
      onProgress?.('Loading Kokoro runtime...');
      const mod = await import(/* @vite-ignore */ PRG_TTS_CDN);
      const gpu = 'gpu' in navigator;
      const tts = await mod.KokoroTTS.from_pretrained(PRG_TTS_MODEL, {
        dtype: gpu ? 'fp32' : 'q8',
        device: gpu ? 'webgpu' : 'wasm',
        progress_callback: p => {
          const file = p.file ? p.file.split('/').pop() : '';
          if (p.status === 'progress' && file && typeof p.progress === 'number') {
            onProgress?.(`Downloading ${file}: ${Math.round(p.progress)}%`);
          }
        },
      });
      prgTtsOk = true;
      onProgress?.('Voice model ready.');
      return tts;
    })();
    prgTtsPromise.catch(() => { prgTtsPromise = null; prgTtsOk = false; });
  }
  return prgTtsPromise;
}

function prgTtsWav(samples, rate) {
  const buf = new ArrayBuffer(44 + samples.length * 2);
  const v = new DataView(buf);
  let o = 0;
  const str = s => { for (let i = 0; i < s.length; i++) v.setUint8(o++, s.charCodeAt(i)); };
  str('RIFF'); v.setUint32(o, 36 + samples.length * 2, true); o += 4;
  str('WAVEfmt '); v.setUint32(o, 16, true); o += 4;
  v.setUint16(o, 1, true); o += 2;
  v.setUint16(o, 1, true); o += 2;
  v.setUint32(o, rate, true); o += 4;
  v.setUint32(o, rate * 2, true); o += 4;
  v.setUint16(o, 2, true); o += 2;
  v.setUint16(o, 16, true); o += 2;
  str('data'); v.setUint32(o, samples.length * 2, true); o += 4;
  for (let i = 0; i < samples.length; i++, o += 2) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(o, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Blob([v], { type: 'audio/wav' });
}

export async function prgTtsSynth(text, voice, speed, onProgress) {
  if (!prgIsVoice(voice)) throw new Error('Select a voice first.');
  const tts = await prgTtsLoad(onProgress);
  onProgress?.('Synthesizing...');
  const out = await tts.generate(text, { voice, speed: speed || 1 });
  return URL.createObjectURL(prgTtsWav(out.audio, out.sampling_rate));
}

export async function prgTtsTry(voice, speed, onProgress) {
  if (prgTtsPreviewAudio) { prgTtsPreviewAudio.pause(); prgTtsPreviewAudio = null; }
  const url = await prgTtsSynth(PRG_TTS_TRY, voice, speed, onProgress);
  const a = new Audio(url);
  prgTtsPreviewAudio = a;
  a.onended = () => { URL.revokeObjectURL(url); prgTtsPreviewAudio = null; onProgress?.('Ready.'); };
  await a.play();
  onProgress?.('Playing sample...');
}

/**
 * Plays narration sections one at a time and calls onActivate(section|null)
 * so the host can highlight and scroll to the note being spoken.
 * opts: getVoice() -> {voice, rate}, setStatus(msg), onActivate(section), onState()
 */
export function prgCreatePlayer(opts) {
  let sections = [];
  let index = 0;
  let playing = false;
  let chain = false;
  let token = 0;
  let audio = null;
  const urls = new Map();
  const status = m => opts.setStatus?.(m);

  const clear = () => {
    token++;
    if (audio) { audio.pause(); audio.removeAttribute('src'); audio = null; }
    for (const u of urls.values()) URL.revokeObjectURL(u);
    urls.clear();
  };
  const ensure = async i => {
    const sec = sections[i];
    const { voice, rate } = opts.getVoice();
    if (!sec) return null;
    if (urls.has(sec.id)) return urls.get(sec.id);
    const u = await prgTtsSynth(sec.text, voice, rate, status);
    urls.set(sec.id, u);
    return u;
  };
  const finish = () => { playing = false; chain = false; opts.onActivate?.(null); opts.onState?.(); };

  const speakAt = (i, auto) => {
    if (i < 0 || i >= sections.length) return;
    const mine = ++token;
    index = i; playing = true; chain = auto;
    const sec = sections[i];
    opts.onActivate?.(sec);
    status(`Preparing "${sec.label}"...`);
    opts.onState?.();
    ensure(i).then(async url => {
      if (mine !== token || !url) return;
      if (audio) { audio.pause(); audio.removeAttribute('src'); }
      audio = new Audio(url);
      audio.onended = () => {
        if (mine !== token) return;
        if (playing && chain && index < sections.length - 1) { speakAt(index + 1, true); return; }
        finish();
        status(`${sections.length} talking points`);
      };
      status(`Speaking ${i + 1}/${sections.length}: ${sec.label}`);
      await audio.play();
      opts.onState?.();
      if (i + 1 < sections.length) ensure(i + 1).catch(() => {});
    }).catch(err => {
      if (mine !== token) return;
      finish();
      status(err.message);
    });
  };

  const needVoice = () => {
    if (prgIsVoice(opts.getVoice().voice)) return false;
    status('Pick a voice, then Download it.');
    return true;
  };

  return {
    setSections(next) { sections = next || []; index = 0; playing = false; chain = false; clear(); opts.onActivate?.(null); opts.onState?.(); },
    hasSections: () => sections.length > 0,
    isPlaying: () => playing,
    canPrev: () => index > 0,
    canNext: () => index < sections.length - 1,
    play() {
      if (!sections.length || needVoice()) return;
      if (audio && audio.currentTime > 0 && !audio.ended) {
        playing = true; chain = true;
        opts.onActivate?.(sections[index]);
        audio.play().catch(() => {});
        opts.onState?.();
        return;
      }
      speakAt(Math.min(index, sections.length - 1), true);
    },
    pause() { if (audio) audio.pause(); finish(); },
    stop() { token++; if (audio) { audio.pause(); audio.currentTime = 0; } finish(); },
    prev() { if (index > 0) speakAt(index - 1, playing); },
    next() { if (index < sections.length - 1) speakAt(index + 1, playing); },
    playAt(id) {
      const i = sections.findIndex(s => s.id === id);
      if (i >= 0 && !needVoice()) speakAt(i, false);
    },
    voiceChanged() { clear(); },
  };
}
