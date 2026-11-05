import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Activity, Users, RefreshCw, Circle, Clock3, ListOrdered, Search, AlertCircle } from 'lucide-react';
import { getUsage, type UsageSnapshot } from '@/api/status';

const REFRESH_MS = 2000;

export default function LiveUsage() {
  const { t, i18n } = useTranslation();
  const [data, setData] = useState<UsageSnapshot | null>(null);
  const [failed, setFailed] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [generation, setGeneration] = useState(0);
  const [query, setQuery] = useState('');
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;
    async function refresh() {
      if (document.hidden) {
        timer = setTimeout(refresh, REFRESH_MS);
        return;
      }
      controller = new AbortController();
      const timeout = setTimeout(() => controller?.abort(), 8000);
      setRefreshing(true);
      try {
        const next = await getUsage(controller.signal);
        if (!disposed) { setData(next); setFailed(false); }
      } catch {
        if (!disposed) setFailed(true);
      } finally {
        clearTimeout(timeout);
        if (!disposed) {
          setRefreshing(false);
          timer = setTimeout(refresh, REFRESH_MS);
        }
      }
    }
    void refresh();
    const manual = () => setGeneration(g => g + 1);
    window.addEventListener('cc:refresh', manual);
    return () => {
      disposed = true;
      clearTimeout(timer);
      controller?.abort();
      window.removeEventListener('cc:refresh', manual);
    };
  }, [generation]);

  const clock = (value: string) => new Date(value).toLocaleTimeString(i18n.language, { hour12: false });
  const filtered = (data?.users || []).filter(user =>
    [user.user_name, user.user_id, user.platform, ...user.projects].join(' ').toLocaleLowerCase().includes(query.toLocaleLowerCase())
  );
  const visible = expanded ? filtered : filtered.slice(0, 8);
  const stats = [
    { key: 'using', value: data?.using_users, icon: Users, accent: true },
    { key: 'recent', value: data?.recent_users, icon: Clock3 },
    { key: 'processing', value: data?.processing_sessions, icon: Activity },
    { key: 'queued', value: data?.queued_messages, icon: ListOrdered },
  ];

  return (
    <section className="rounded-2xl border border-gray-200/90 dark:border-white/[0.08] bg-white/80 dark:bg-white/[0.02] overflow-hidden" aria-labelledby="live-usage-title">
      <div className="p-5 sm:p-6 border-b border-gray-100 dark:border-white/[0.06]">
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2.5 mb-1.5">
              <h2 id="live-usage-title" className="text-base font-semibold text-gray-900 dark:text-white">{t('usage.title')}</h2>
              <span className={`inline-flex items-center gap-1.5 text-[11px] font-medium px-2 py-0.5 rounded-full ${failed ? 'text-amber-700 bg-amber-50 dark:text-amber-300 dark:bg-amber-900/20' : 'text-emerald-700 bg-emerald-50 dark:text-emerald-300 dark:bg-emerald-900/20'}`}>
                <Circle size={6} className="fill-current" />{t(failed ? 'usage.reconnecting' : 'usage.live')}
              </span>
            </div>
            <p className="text-xs text-gray-500 dark:text-gray-400 max-w-2xl leading-relaxed">{t('usage.description')}</p>
          </div>
          <button type="button" title={t('usage.refresh')} aria-label={t('usage.refresh')} onClick={() => setGeneration(g => g + 1)} disabled={refreshing} className="shrink-0 p-2 rounded-lg border border-gray-200 dark:border-gray-700 text-gray-500 hover:text-accent hover:border-accent/40 disabled:opacity-40 transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent">
            <RefreshCw size={15} className={refreshing ? 'animate-spin' : ''} />
          </button>
        </div>
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mt-5">
          {stats.map(({ key, value, icon: Icon, accent }) => (
            <div key={key} className={`rounded-xl p-4 ${accent ? 'bg-accent/10 border border-accent/15' : 'bg-gray-50/90 dark:bg-white/[0.025] border border-gray-100 dark:border-white/[0.04]'}`}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-medium text-gray-500 dark:text-gray-400">{t(`usage.${key}`)}</span>
                <Icon size={14} className={accent ? 'text-accent' : 'text-gray-400'} />
              </div>
              <p className={`mt-2 text-3xl font-semibold tabular-nums tracking-tight ${accent ? 'text-accent' : 'text-gray-900 dark:text-white'}`}>{value ?? '—'}</p>
              <p className="mt-1 text-[11px] text-gray-500 dark:text-gray-400 leading-relaxed">{t(`usage.${key}Hint`)}</p>
            </div>
          ))}
        </div>
      </div>
      {failed && <div role="status" className="flex items-start gap-2 px-5 sm:px-6 py-3 text-xs text-amber-800 dark:text-amber-200 bg-amber-50 dark:bg-amber-900/20"><AlertCircle size={14} className="shrink-0" />{t(data ? 'usage.stale' : 'usage.unavailable')}</div>}
      <div className="px-5 sm:px-6 py-4 flex items-center justify-between flex-wrap gap-3">
        <h3 className="text-xs font-semibold text-gray-700 dark:text-gray-200">{t('usage.people')}</h3>
        <label className="flex items-center gap-2 text-gray-400 bg-gray-50 dark:bg-white/[0.04] px-2.5 py-1.5 rounded-lg border border-gray-100 dark:border-white/[0.06]">
          <Search size={13} />
          <input aria-label={t('usage.search')} placeholder={t('usage.search')} value={query} onChange={e => { setQuery(e.target.value); setExpanded(false); }} className="w-44 sm:w-52 bg-transparent text-xs text-gray-700 dark:text-gray-200 outline-none placeholder:text-gray-400" />
        </label>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-xs text-left">
          <thead className="text-gray-400 bg-gray-50/60 dark:bg-white/[0.015] border-y border-gray-100 dark:border-white/[0.05]">
            <tr>{['user', 'platform', 'project', 'state', 'lastSeen'].map(key => <th key={key} scope="col" className="px-5 sm:px-6 py-2.5 font-medium whitespace-nowrap">{t(`usage.${key}`)}</th>)}</tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-white/[0.05]">
            {visible.map(user => {
              const state = user.processing_sessions > 0 ? 'working' : user.queued_messages > 0 ? 'waiting' : 'recentState';
              return <tr key={`${user.platform}:${user.user_id}`} className="hover:bg-gray-50/60 dark:hover:bg-white/[0.02]">
                <td className="px-5 sm:px-6 py-3.5 max-w-64"><Link className="font-medium text-accent hover:underline block truncate" title={t('progress.view')} to={`/progress/${encodeURIComponent(user.projects[0] || '')}?user=${encodeURIComponent(user.user_id)}&platform=${encodeURIComponent(user.platform)}`}>{user.user_name || user.user_id}</Link>{user.user_name && user.user_name !== user.user_id && <p className="font-mono text-[10px] text-gray-400 mt-0.5 truncate" title={user.user_id}>{user.user_id}</p>}</td>
                <td className="px-5 sm:px-6 py-3.5 text-gray-500 whitespace-nowrap">{user.platform}</td>
                <td className="px-5 sm:px-6 py-3.5 text-gray-500 max-w-52"><p className="truncate" title={user.projects.join(', ')}>{user.projects.join(', ')}</p></td>
                <td className="px-5 sm:px-6 py-3.5 whitespace-nowrap"><span className={`inline-flex items-center gap-1.5 ${state === 'working' ? 'text-emerald-600 dark:text-emerald-400' : state === 'waiting' ? 'text-amber-600 dark:text-amber-400' : 'text-gray-400'}`}><Circle size={6} className="fill-current" />{t(`usage.${state}`)}</span>{user.queued_messages > 0 && state === 'working' && <span className="text-gray-400 ml-2">{t('usage.queueCount', { count: user.queued_messages })}</span>}</td>
                <td className="px-5 sm:px-6 py-3.5 text-gray-500 tabular-nums whitespace-nowrap">{user.last_seen.startsWith('0001-') ? '—' : clock(user.last_seen)}</td>
              </tr>;
            })}
            {visible.length === 0 && <tr><td colSpan={5} className="text-center text-gray-400 py-10 px-6">{t(!data ? failed ? 'usage.unavailable' : 'usage.loading' : query ? 'usage.noMatches' : 'usage.empty')}</td></tr>}
          </tbody>
        </table>
      </div>
      <div className="px-5 sm:px-6 py-3 flex items-center justify-between flex-wrap gap-2 border-t border-gray-100 dark:border-white/[0.06] text-[11px] text-gray-400">
        <span aria-live="polite">{data ? t('usage.updated', { time: clock(data.sampled_at) }) : t('usage.loading')}</span>
        {filtered.length > 8 && <button type="button" onClick={() => setExpanded(!expanded)} className="text-accent hover:underline">{t(expanded ? 'usage.collapse' : 'usage.showAll', { count: filtered.length })}</button>}
      </div>
    </section>
  );
}
