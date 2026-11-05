import { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Activity, ArrowLeft, Circle, Clock3, Eye, RefreshCw, Search, Wrench, AlertCircle } from 'lucide-react';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { getProjectProgress, type ProgressSnapshot } from '@/api/sessions';

export default function SessionProgress() {
  const { name = '' } = useParams<{ name: string }>();
  const { t, i18n } = useTranslation();
  const [search, setSearch] = useSearchParams();
  const key = search.get('key') || '';
  const user = search.get('user') || '';
  const platform = search.get('platform') || '';
  const [data, setData] = useState<ProgressSnapshot | null>(null);
  const [error, setError] = useState(false);
  const [generation, setGeneration] = useState(0);
  const [filter, setFilter] = useState('');

  useEffect(() => {
    let disposed = false;
    setData(null);
    setError(false);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;
    async function refresh() {
      if (document.hidden) { timer = setTimeout(refresh, 1000); return; }
      controller = new AbortController();
      const deadline = setTimeout(() => controller?.abort(), 8000);
      try {
        const params: Record<string,string> = {};
        if (key) params.key = key;
        if (user) params.user = user;
        if (platform) params.platform = platform;
        const next = await getProjectProgress(name, params, controller.signal);
        if (!disposed) { setData(next); setError(false); }
      } catch (err: any) {
        if (!disposed) {
          setError(true);
          if (err.status === 404 && key) { const next = new URLSearchParams(search); next.delete('key'); setSearch(next, { replace: true }); }
        }
      } finally {
        clearTimeout(deadline);
        if (!disposed) timer = setTimeout(refresh, 1000);
      }
    }
    void refresh();
    return () => { disposed = true; clearTimeout(timer); controller?.abort(); };
  }, [name, key, user, platform, generation]);

  const selected = data?.selected;
  const rows = (data?.sessions || []).filter(s => [s.name,s.user_name,s.user_id,s.chat_name,s.session_id].join(' ').toLocaleLowerCase().includes(filter.toLocaleLowerCase()));
  const clock = (value: string) => new Date(value).toLocaleTimeString(i18n.language, { hour12: false });
  const status = (value: string) => t(`progress.status.${value}`, { defaultValue: value });
  const pick = (value: string) => { const next = new URLSearchParams(search); next.set('key', value); setSearch(next); };

  return (
    <div className="space-y-4 animate-fade-in">
      <header className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <Link to="/chat" aria-label={t('progress.back')} className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-800"><ArrowLeft size={18} className="text-gray-400" /></Link>
          <div className="min-w-0"><h2 className="text-lg font-semibold text-gray-900 dark:text-white truncate">{name}</h2><p className="text-xs text-gray-500 mt-0.5 flex items-center gap-1.5"><Eye size={12} />{t('progress.readOnly')}</p></div>
        </div>
        <button type="button" onClick={() => setGeneration(g=>g+1)} aria-label={t('usage.refresh')} className="p-2 border rounded-lg border-gray-200 dark:border-gray-700 text-gray-500 hover:text-accent"><RefreshCw size={15} /></button>
      </header>
      <div className="rounded-xl bg-accent/5 border border-accent/15 px-4 py-3 text-xs text-gray-600 dark:text-gray-300 flex gap-2"><Eye size={15} className="text-accent shrink-0" />{t('progress.description')}</div>
      {error && <div role="status" className="rounded-lg px-4 py-3 bg-amber-50 dark:bg-amber-900/20 text-amber-700 dark:text-amber-300 text-xs flex gap-2"><AlertCircle size={14} />{t(data ? 'usage.stale' : 'usage.unavailable')}</div>}
      <div className="grid lg:grid-cols-[280px_minmax(0,1fr)] gap-4 items-start">
        <aside className="border border-gray-200 dark:border-white/[0.08] rounded-xl bg-white dark:bg-white/[0.02] overflow-hidden">
          <div className="p-3 border-b border-gray-100 dark:border-white/[0.06] flex gap-2 items-center text-gray-400"><Search size={13} /><input aria-label={t('progress.search')} placeholder={t('progress.search')} value={filter} onChange={e=>setFilter(e.target.value)} className="text-xs bg-transparent outline-none w-full text-gray-700 dark:text-gray-200" /></div>
          {user && <button type="button" onClick={()=>setSearch({})} className="text-xs text-accent px-3 py-2 hover:underline">{t('progress.allSessions')}</button>}
          <div className="max-h-[65vh] overflow-y-auto p-2 space-y-1">
            {rows.map(s=><button type="button" key={s.key} onClick={()=>pick(s.key)} className={`w-full text-left p-3 rounded-lg transition-colors ${s.key===selected?.key ? 'bg-accent/10 ring-1 ring-accent/20' : 'hover:bg-gray-50 dark:hover:bg-white/[0.04]'}`}>
              <div className="flex gap-2 items-center text-xs font-medium text-gray-800 dark:text-gray-100"><Circle size={6} className={`shrink-0 fill-current ${s.finished ? 'text-gray-300' : 'text-emerald-500'}`} /><span className="truncate">{s.user_name || s.name || s.session_id}</span><span className="ml-auto text-[10px] text-gray-400 shrink-0">{s.session_id}</span></div>
              <p className="text-[11px] text-gray-400 truncate mt-1.5">{s.chat_name || s.platform} · {status(s.status)}</p>
              {s.request && <p className="text-[11px] text-gray-500 line-clamp-2 mt-1.5 leading-relaxed">{s.request}</p>}
            </button>)}
            {!rows.length && <p className="text-xs text-gray-400 p-5 text-center">{t(data ? 'progress.noSessions' : 'usage.loading')}</p>}
          </div>
        </aside>
        <main className="space-y-4 min-w-0">
          {!selected ? <div className="rounded-xl border border-gray-200 dark:border-white/[0.08] p-12 text-center text-sm text-gray-400">{t(data ? 'progress.noSessions' : 'usage.loading')}</div> : <>
            <div className="rounded-xl border border-gray-200 dark:border-white/[0.08] bg-white dark:bg-white/[0.02] p-5">
              <div className="flex flex-wrap gap-3 items-center justify-between"><h3 className="font-medium text-gray-900 dark:text-white text-sm">{selected.user_name || selected.name || selected.session_id}</h3><span className={`inline-flex gap-1.5 items-center text-xs ${selected.status==='error' ? 'text-red-500' : selected.finished ? 'text-gray-400' : 'text-emerald-600 dark:text-emerald-400'}`}><Activity size={13} className={selected.finished ? '' : 'animate-pulse'} />{status(selected.status)}</span></div>
              <div className="flex flex-wrap gap-x-5 gap-y-2 text-[11px] text-gray-400 mt-3"><span>{selected.platform} · {selected.session_id}</span><span className="flex items-center gap-1"><Wrench size={11} />{t('progress.toolCount',{count:selected.tool_count})}</span><span className="flex items-center gap-1"><Clock3 size={11} />{clock(selected.updated_at)}</span></div>
              {selected.request && <div className="mt-4 border-t border-gray-100 dark:border-white/[0.06] pt-3"><p className="text-[11px] text-gray-400 mb-1.5">{t('progress.request')}</p><p className="text-sm text-gray-700 dark:text-gray-200 whitespace-pre-wrap break-words">{selected.request}</p></div>}
            </div>
            <section className="rounded-xl border border-gray-200 dark:border-white/[0.08] bg-white dark:bg-white/[0.02] p-5">
              <h3 className="text-sm font-medium text-gray-900 dark:text-white mb-4">{t('progress.timeline')}</h3>
              {!(selected.events?.length) && <p className="text-xs text-gray-400 py-4">{t(selected.status==='idle' ? 'progress.historical' : 'progress.waitingEvents')}</p>}
              <ol className="space-y-3 max-h-[45vh] overflow-y-auto">
                {selected.events?.map((event,i)=><li key={`${selected.turn}-${i}`} className="border-l-2 border-gray-100 dark:border-gray-700 pl-3">
                  <div className="flex items-center flex-wrap gap-2 text-xs"><span className="text-gray-700 dark:text-gray-200 font-medium">{t(`progress.event.${event.kind}`,{defaultValue:event.kind})}</span>{event.tool && <span className="font-mono text-accent">{event.tool}</span>}{event.status && <span className="text-gray-400">{event.status}</span>}<time className="text-[10px] text-gray-400 ml-auto tabular-nums">{clock(event.time)}</time></div>
                  {event.text && <pre className="mt-2 p-2.5 text-[11px] bg-gray-50 dark:bg-black/20 rounded-lg whitespace-pre-wrap break-words text-gray-500 dark:text-gray-400 font-mono max-h-40 overflow-auto">{event.text}</pre>}
                </li>)}
              </ol>
              {(selected.status==='waiting_permission' || selected.status==='waiting_input') && <p className="mt-4 text-xs text-amber-600 dark:text-amber-300">{t('progress.respondElsewhere')}</p>}
            </section>
            {(selected.preview || selected.result) && <section className="rounded-xl border border-gray-200 dark:border-white/[0.08] bg-white dark:bg-white/[0.02] p-5"><h3 className="text-sm font-medium text-gray-900 dark:text-white mb-3">{t(selected.result ? 'progress.result' : 'progress.output')}</h3><div className="prose prose-sm dark:prose-invert max-w-none break-words"><Markdown remarkPlugins={[remarkGfm]}>{selected.result || selected.preview || ''}</Markdown></div></section>}
            {!!selected.history?.length && <details className="rounded-xl border border-gray-200 dark:border-white/[0.08] bg-white dark:bg-white/[0.02] p-5"><summary className="text-sm font-medium text-gray-700 dark:text-gray-200 cursor-pointer">{t('progress.history')}</summary><div className="mt-4 space-y-4 max-h-[65vh] overflow-y-auto">{selected.history.map((h,i)=><div key={i} className="text-sm border-t border-gray-100 dark:border-white/[0.05] pt-3"><p className="text-[11px] text-gray-400 mb-2">{t(h.role === 'user' ? 'usage.user' : 'progress.assistant')} · {clock(h.timestamp)}</p><div className="prose prose-sm dark:prose-invert max-w-none break-words"><Markdown remarkPlugins={[remarkGfm]}>{h.content}</Markdown></div></div>)}</div></details>}
          </>}
          <p className="text-[11px] text-gray-400">{data ? t('progress.updated',{time:clock(data.sampled_at)}) : t('usage.loading')}</p>
        </main>
      </div>
    </div>
  );
}
