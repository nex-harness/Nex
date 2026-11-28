import { Check, LoaderCircle, Mic, RotateCcw, Square, X } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';

interface SpeechRecognitionResultEvent {
  resultIndex: number;
  results: ArrayLike<{
    isFinal: boolean;
    0: { transcript: string };
  }>;
}

interface SpeechRecognitionLike {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((ev: SpeechRecognitionResultEvent) => void) | null;
  onerror: ((ev: { error?: string }) => void) | null;
  onend: (() => void) | null;
  start: () => void;
  stop: () => void;
  abort: () => void;
}

type SpeechRecognitionCtor = new () => SpeechRecognitionLike;

function getSpeechRecognitionCtor(): SpeechRecognitionCtor | null {
  if (typeof window === 'undefined') return null;
  const w = window as unknown as {
    SpeechRecognition?: SpeechRecognitionCtor;
    webkitSpeechRecognition?: SpeechRecognitionCtor;
  };
  return w.SpeechRecognition || w.webkitSpeechRecognition || null;
}

function pickAudioMimeType(): string {
  if (typeof MediaRecorder === 'undefined' || typeof MediaRecorder.isTypeSupported !== 'function') {
    return '';
  }
  const candidates = [
    'audio/webm;codecs=opus',
    'audio/webm',
    'audio/mp4',
    'audio/ogg;codecs=opus',
  ];
  for (const c of candidates) {
    if (MediaRecorder.isTypeSupported(c)) return c;
  }
  return '';
}

function mimeToExt(mime: string): string {
  if (mime.includes('mp4')) return 'm4a';
  if (mime.includes('ogg')) return 'ogg';
  return 'webm';
}

function formatDuration(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

export interface VoiceInputButtonProps {
  disabled?: boolean;
  onTranscript: (text: string, autoSend: boolean) => void;
}

export function VoiceInputButton({ disabled, onTranscript }: VoiceInputButtonProps) {
  const locale = useNexStore((s) => s.locale);
  const preferences = useNexStore((s) => s.preferences);

  const sttEnabled = preferences?.stt_enabled !== false;
  const enginePref = preferences?.stt_engine || 'auto';
  const hasApiConfig = Boolean(preferences?.stt_base_url?.trim());
  const sttLang = preferences?.stt_language || '';
  const autoSend = Boolean(preferences?.stt_auto_send);

  const [phase, setPhase] = useState<'idle' | 'recording' | 'transcribing' | 'error'>('idle');
  const [elapsed, setElapsed] = useState(0);
  const [levels, setLevels] = useState<number[]>([0.2, 0.2, 0.2, 0.2, 0.2]);
  const [interimText, setInterimText] = useState('');
  const [errorMsg, setErrorMsg] = useState('');

  const mediaRecorderRef = useRef<MediaRecorder | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const lastBlobRef = useRef<{ blob: Blob; mime: string } | null>(null);
  const audioCtxRef = useRef<AudioContext | null>(null);
  const rafRef = useRef<number>(0);
  const timerRef = useRef<number>(0);
  const cancelledRef = useRef(false);
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null);
  const finalTranscriptRef = useRef('');

  const cleanupMedia = useCallback(() => {
    if (rafRef.current) {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = 0;
    }
    if (timerRef.current) {
      window.clearInterval(timerRef.current);
      timerRef.current = 0;
    }
    if (audioCtxRef.current) {
      void audioCtxRef.current.close().catch(() => {});
      audioCtxRef.current = null;
    }
    if (streamRef.current) {
      for (const t of streamRef.current.getTracks()) t.stop();
      streamRef.current = null;
    }
    mediaRecorderRef.current = null;
  }, []);

  useEffect(() => {
    return () => {
      cleanupMedia();
      if (recognitionRef.current) {
        try {
          recognitionRef.current.abort();
        } catch {
          // ignore
        }
        recognitionRef.current = null;
      }
    };
  }, [cleanupMedia]);

  const uploadAndTranscribe = useCallback(
    async (blob: Blob, mime: string) => {
      setPhase('transcribing');
      setErrorMsg('');
      try {
        const fd = new FormData();
        const ext = mimeToExt(mime);
        fd.append('file', blob, `voice.${ext}`);
        if (sttLang) {
          fd.append('language', sttLang);
        }
        const res = await fetch('/api/v1/audio/transcribe', {
          method: 'POST',
          body: fd,
        });
        const data = (await res.json().catch(() => ({}))) as { text?: string; error?: string };
        if (!res.ok) {
          throw new Error(data.error || `HTTP ${res.status}`);
        }
        const text = (data.text || '').trim();
        lastBlobRef.current = null;
        setPhase('idle');
        if (text) {
          onTranscript(text, autoSend);
        }
      } catch (err) {
        setErrorMsg(err instanceof Error ? err.message : String(err));
        setPhase('error');
      }
    },
    [autoSend, onTranscript, sttLang],
  );

  const startBrowserSpeech = useCallback(() => {
    const Ctor = getSpeechRecognitionCtor();
    if (!Ctor) {
      setErrorMsg(formatMsg(locale, 'chat.voiceNotSupported'));
      setPhase('error');
      return;
    }
    cancelledRef.current = false;
    finalTranscriptRef.current = '';
    setInterimText('');
    setElapsed(0);
    setErrorMsg('');

    const rec = new Ctor();
    rec.continuous = true;
    rec.interimResults = true;
    if (sttLang === 'zh') rec.lang = 'zh-CN';
    else if (sttLang === 'en') rec.lang = 'en-US';
    else if (sttLang === 'ja') rec.lang = 'ja-JP';
    else rec.lang = locale;

    rec.onresult = (ev) => {
      let interim = '';
      for (let i = ev.resultIndex; i < ev.results.length; i++) {
        const item = ev.results[i];
        const t = item?.[0]?.transcript || '';
        if (item.isFinal) {
          finalTranscriptRef.current += t;
        } else {
          interim += t;
        }
      }
      setInterimText((finalTranscriptRef.current + interim).trim());
    };
    rec.onerror = (ev) => {
      if (cancelledRef.current) return;
      if (timerRef.current) {
        window.clearInterval(timerRef.current);
        timerRef.current = 0;
      }
      setErrorMsg(ev.error || formatMsg(locale, 'chat.voiceFailed'));
      setPhase('error');
      recognitionRef.current = null;
    };
    rec.onend = () => {
      if (timerRef.current) {
        window.clearInterval(timerRef.current);
        timerRef.current = 0;
      }
      recognitionRef.current = null;
      if (cancelledRef.current) {
        setPhase('idle');
        setInterimText('');
        return;
      }
      const text = finalTranscriptRef.current.trim();
      setInterimText('');
      setPhase('idle');
      if (text) {
        onTranscript(text, autoSend);
      }
    };

    recognitionRef.current = rec;
    setPhase('recording');
    timerRef.current = window.setInterval(() => setElapsed((e) => e + 1), 1000);
    try {
      rec.start();
    } catch (err) {
      if (timerRef.current) {
        window.clearInterval(timerRef.current);
        timerRef.current = 0;
      }
      recognitionRef.current = null;
      setErrorMsg(err instanceof Error ? err.message : String(err));
      setPhase('error');
    }
  }, [autoSend, locale, onTranscript, sttLang]);

  const startApiRecording = useCallback(async () => {
    if (typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      setErrorMsg(formatMsg(locale, 'chat.voiceNotSupported'));
      setPhase('error');
      return;
    }
    cancelledRef.current = false;
    chunksRef.current = [];
    setElapsed(0);
    setErrorMsg('');

    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      streamRef.current = stream;

      const mimeType = pickAudioMimeType();
      const mr = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
      mediaRecorderRef.current = mr;

      mr.ondataavailable = (ev) => {
        if (ev.data && ev.data.size > 0) {
          chunksRef.current.push(ev.data);
        }
      };
      mr.onstop = () => {
        const finalMime = mr.mimeType || mimeType || 'audio/webm';
        const blob = new Blob(chunksRef.current, { type: finalMime });
        chunksRef.current = [];
        cleanupMedia();
        if (cancelledRef.current || blob.size === 0) {
          setPhase('idle');
          return;
        }
        lastBlobRef.current = { blob, mime: finalMime };
        void uploadAndTranscribe(blob, finalMime);
      };

      // Live audio level meter
      if (typeof AudioContext !== 'undefined') {
        try {
          const ctx = new AudioContext();
          audioCtxRef.current = ctx;
          const src = ctx.createMediaStreamSource(stream);
          const analyser = ctx.createAnalyser();
          analyser.fftSize = 32;
          src.connect(analyser);
          const buf = new Uint8Array(analyser.frequencyBinCount);
          const tick = () => {
            analyser.getByteFrequencyData(buf);
            const bars = [1, 3, 5, 7, 9].map((idx) => {
              const val = buf[idx] || 0;
              return Math.max(0.18, Math.min(1, val / 180));
            });
            setLevels(bars);
            rafRef.current = requestAnimationFrame(tick);
          };
          rafRef.current = requestAnimationFrame(tick);
        } catch {
          // AudioContext optional
        }
      }

      mr.start(200);
      setPhase('recording');
      timerRef.current = window.setInterval(() => setElapsed((e) => e + 1), 1000);
    } catch (err) {
      cleanupMedia();
      setErrorMsg(
        err instanceof Error && err.name === 'NotAllowedError'
          ? formatMsg(locale, 'chat.voicePermissionDenied')
          : err instanceof Error
            ? err.message
            : formatMsg(locale, 'chat.voiceFailed'),
      );
      setPhase('error');
    }
  }, [cleanupMedia, locale, uploadAndTranscribe]);

  const startRecording = useCallback(() => {
    if (disabled || phase === 'transcribing') return;
    const useApi =
      enginePref === 'api' || (enginePref === 'auto' && hasApiConfig);
    if (useApi) {
      void startApiRecording();
    } else {
      startBrowserSpeech();
    }
  }, [disabled, enginePref, hasApiConfig, phase, startApiRecording, startBrowserSpeech]);

  const stopRecording = useCallback(() => {
    if (recognitionRef.current) {
      try {
        recognitionRef.current.stop();
      } catch {
        // ignore
      }
      return;
    }
    if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
      mediaRecorderRef.current.stop();
    }
  }, []);

  const cancelRecording = useCallback(() => {
    cancelledRef.current = true;
    if (recognitionRef.current) {
      try {
        recognitionRef.current.abort();
      } catch {
        // ignore
      }
      recognitionRef.current = null;
    }
    if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
      mediaRecorderRef.current.stop();
    } else {
      cleanupMedia();
    }
    setInterimText('');
    setPhase('idle');
  }, [cleanupMedia]);

  // Option/Alt + V shortcut to toggle voice recording
  useEffect(() => {
    if (!sttEnabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.altKey && !e.metaKey && !e.ctrlKey && e.code === 'KeyV') {
        e.preventDefault();
        if (phase === 'recording') {
          stopRecording();
        } else if (phase === 'idle' || phase === 'error') {
          startRecording();
        }
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [phase, startRecording, stopRecording, sttEnabled]);

  if (!sttEnabled) return null;

  if (phase === 'recording') {
    return (
      <div
        data-testid="voice-recording-bar"
        className="inline-flex h-8 items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-2.5 text-ui-xs text-foreground"
      >
        <span className="flex items-end gap-0.5" aria-hidden="true">
          {levels.map((lv, i) => (
            <span
              key={i}
              className="w-0.5 rounded-full bg-destructive transition-all duration-75"
              style={{ height: `${Math.round(lv * 14)}px` }}
            />
          ))}
        </span>
        <span className="font-mono tabular-nums text-destructive">{formatDuration(elapsed)}</span>
        {interimText && (
          <span className="max-w-[160px] truncate text-foreground-subtle" title={interimText}>
            {interimText}
          </span>
        )}
        <button
          type="button"
          onClick={cancelRecording}
          title={formatMsg(locale, 'common.cancel')}
          aria-label={formatMsg(locale, 'common.cancel')}
          className="flex size-5 items-center justify-center rounded text-foreground-subtle hover:bg-hover hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={stopRecording}
          title={formatMsg(locale, 'chat.voiceFinish')}
          aria-label={formatMsg(locale, 'chat.voiceFinish')}
          className="flex size-5 items-center justify-center rounded bg-destructive text-white hover:opacity-90"
        >
          {recognitionRef.current ? <Check className="size-3" /> : <Square className="size-2.5 fill-current" />}
        </button>
      </div>
    );
  }

  if (phase === 'transcribing') {
    return (
      <div
        data-testid="voice-transcribing-indicator"
        className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-2.5 text-ui-xs text-foreground-subtle"
      >
        <LoaderCircle className="size-3.5 animate-spin" />
        <span>{formatMsg(locale, 'chat.voiceTranscribing')}</span>
      </div>
    );
  }

  if (phase === 'error') {
    return (
      <div
        data-testid="voice-error-bar"
        className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-destructive/40 bg-destructive/10 px-2 text-ui-xs text-destructive"
      >
        <span className="max-w-[160px] truncate" title={errorMsg}>
          {errorMsg || formatMsg(locale, 'chat.voiceFailed')}
        </span>
        {lastBlobRef.current && (
          <button
            type="button"
            onClick={() => {
              const saved = lastBlobRef.current;
              if (saved) void uploadAndTranscribe(saved.blob, saved.mime);
            }}
            title={formatMsg(locale, 'chat.voiceRetry')}
            aria-label={formatMsg(locale, 'chat.voiceRetry')}
            className="flex size-5 items-center justify-center rounded hover:bg-destructive/20"
          >
            <RotateCcw className="size-3" />
          </button>
        )}
        <button
          type="button"
          onClick={() => {
            setErrorMsg('');
            setPhase('idle');
          }}
          aria-label={formatMsg(locale, 'common.cancel')}
          className="flex size-5 items-center justify-center rounded hover:bg-destructive/20"
        >
          <X className="size-3" />
        </button>
      </div>
    );
  }

  return (
    <button
      type="button"
      data-testid="voice-input-btn"
      disabled={disabled}
      onClick={startRecording}
      title={formatMsg(locale, 'chat.voiceInputTooltip')}
      aria-label={formatMsg(locale, 'chat.voiceInput')}
      className="flex size-8 items-center justify-center rounded-lg text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
    >
      <Mic className="size-4" />
    </button>
  );
}
