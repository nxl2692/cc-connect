import { useTranslation } from 'react-i18next';
import { Clock3, ListOrdered } from 'lucide-react';
import type { ProgressQueuedRequest } from '@/api/sessions';

export default function QueueProgress({ requests, sampledAt }: { requests: ProgressQueuedRequest[]; sampledAt: string }) {
  const { t, i18n } = useTranslation();
  const groups = new Map<string, ProgressQueuedRequest[]>();
  for (const request of requests) {
    const rows = groups.get(request.queue_key) || [];
    rows.push(request);
    groups.set(request.queue_key, rows);
  }
  const waitTime = (queuedAt: string) => {
    const seconds = Math.max(0, Math.floor((Date.parse(sampledAt) - Date.parse(queuedAt)) / 1000));
    return t('progress.queue.waiting', { minutes: Math.floor(seconds / 60), seconds: String(seconds % 60).padStart(2, '0') });
  };
  return (
    <section id="queue" aria-label={t('progress.queue.title')} className="rounded-xl border border-gray-200 dark:border-white/[0.08] bg-white dark:bg-white/[0.02] p-4 sm:p-5 scroll-mt-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="flex items-center gap-2 text-sm font-medium text-gray-900 dark:text-white"><ListOrdered size={16} className={requests.length ? 'text-amber-500' : 'text-gray-400'} />{t('progress.queue.title')}</h3>
        <span className={`text-xs tabular-nums ${requests.length ? 'text-amber-600 dark:text-amber-400' : 'text-gray-400'}`}>{t('progress.queue.count', { count: requests.length })}</span>
      </div>
      <p className="text-[11px] text-gray-400 mt-2">{t('progress.queue.description')}</p>
      {!requests.length ? <p className="text-xs text-gray-400 py-4">{t('progress.queue.empty')}</p> : (
        <div className="mt-4 space-y-4 max-h-96 overflow-y-auto pr-1">
          {[...groups.entries()].map(([key, rows], index) => <div key={key}>
            <h4 className="text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-2 break-words" title={rows[0].session_key}>{rows[0].chat_name || `${rows[0].platform} · ${t('progress.queue.group', { number: index + 1 })}`}</h4>
            <ol className="grid md:grid-cols-2 gap-2.5">
              {rows.map(request => <li key={request.key} className="min-w-0 rounded-lg border border-amber-100 dark:border-amber-500/10 bg-amber-50/40 dark:bg-amber-500/[0.03] p-3">
                <div className="flex flex-wrap gap-2 items-center text-xs">
                  <span className="text-amber-600 dark:text-amber-400 font-medium tabular-nums">{t('progress.queue.position', { position: request.position })}</span>
                  <span className="font-medium text-gray-800 dark:text-gray-200 break-all">{request.user_name || request.user_id}</span>
                  {request.queued_at && <span className="flex gap-1 items-center text-[10px] text-gray-400 ml-auto tabular-nums" title={t('progress.queue.queuedAt', { time: new Date(request.queued_at).toLocaleString(i18n.language) })}><Clock3 size={11} />{waitTime(request.queued_at)}</span>}
                </div>
                {request.request && <p className="text-xs text-gray-600 dark:text-gray-300 whitespace-pre-wrap break-words leading-relaxed mt-2">{request.request}</p>}
                {(request.image_count > 0 || request.file_count > 0) && <p className="text-[11px] text-gray-400 mt-2">{t('progress.queue.attachments', { images: request.image_count, files: request.file_count })}</p>}
              </li>)}
            </ol>
          </div>)}
        </div>
      )}
    </section>
  );
}
