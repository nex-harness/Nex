import { formatMsg } from '../i18n/messages';
import { Eye, EyeOff, LoaderCircle, Lock } from 'lucide-react';
import { type FormEvent, useState } from 'react';
import { useNexStore } from '../core/store';

export function AuthGate() {
  const locale = useNexStore((s) => s.locale);
  const loginWithPassword = useNexStore((s) => s.loginWithPassword);

  const [password, setPassword] = useState('');
  const [showPwd, setShowPwd] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!password || submitting) return;
    setSubmitting(true);
    setError('');
    const res = await loginWithPassword(password);
    setSubmitting(false);
    if (!res.ok) {
      setError(formatMsg(locale, 'auth.incorrectPasswordPleaseTryAgain'));
    }
  };

  return (
    <div
      data-testid="auth-gate"
      className="flex h-dvh w-screen items-center justify-center bg-background px-4 text-foreground"
    >
      <div className="w-full max-w-[380px] rounded-2xl border border-border bg-card p-6 shadow-xl">
        <div className="flex flex-col items-center text-center">
          <div className="flex size-11 items-center justify-center rounded-2xl border border-border bg-surface text-foreground shadow-xs">
            <Lock className="size-5" />
          </div>
          <h1 className="mt-4 text-xl font-semibold tracking-tight text-foreground">
            {formatMsg(locale, 'auth.nexAccessProtected')}
          </h1>
          <p className="mt-1.5 text-ui-sm text-foreground-subtle">
            {formatMsg(locale, 'auth.enterYourAccessPasswordTo')}
          </p>
        </div>

        <form onSubmit={(e) => void handleSubmit(e)} className="mt-6 space-y-3.5">
          <div className="relative">
            <input
              autoFocus
              data-testid="auth-password-input"
              type={showPwd ? 'text' : 'password'}
              value={password}
              onChange={(e) => {
                setPassword(e.target.value);
                if (error) setError('');
              }}
              placeholder={formatMsg(locale, 'auth.enterPassword')}
              className="h-10 w-full rounded-xl border border-border bg-input pl-3.5 pr-10 text-ui-base text-foreground placeholder-foreground-subtlest outline-none transition-colors focus:border-brand-blue"
            />
            <button
              type="button"
              onClick={() => setShowPwd((v) => !v)}
              aria-label={showPwd ? 'Hide password' : 'Show password'}
              className="absolute right-2 top-1/2 flex size-7 -translate-y-1/2 items-center justify-center rounded-md text-foreground-subtlest hover:bg-hover hover:text-foreground"
            >
              {showPwd ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </button>
          </div>

          {error && (
            <div
              data-testid="auth-error"
              className="rounded-lg border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-xs text-destructive-surface-fg"
            >
              {error}
            </div>
          )}

          <button
            type="submit"
            data-testid="auth-submit-btn"
            disabled={!password || submitting}
            className="flex h-10 w-full items-center justify-center gap-2 rounded-xl bg-foreground text-ui-base font-medium text-background transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-45"
          >
            {submitting && <LoaderCircle className="size-4 animate-spin" />}
            <span>{submitting ? (formatMsg(locale, 'auth.verifying')) : formatMsg(locale, 'auth.unlock')}</span>
          </button>
        </form>
      </div>
    </div>
  );
}
