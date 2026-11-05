import api from './client';

export interface SystemStatus {
  version: string;
  uptime_seconds: number;
  connected_platforms: string[];
  projects_count: number;
  bridge_adapters: { platform: string; project: string; capabilities: string[] }[];
}

export const getStatus = () => api.get<SystemStatus>('/status');

export interface UsageUser {
  platform: string;
  user_id: string;
  user_name: string;
  last_seen: string;
  started_at?: string;
  projects: string[];
  processing_sessions: number;
  queued_messages: number;
}

export interface UsageSnapshot {
  sampled_at: string;
  window_seconds: number;
  using_users: number;
  recent_users: number;
  processing_sessions: number;
  queued_messages: number;
  users: UsageUser[];
}

export const getUsage = (signal?: AbortSignal) => api.get<UsageSnapshot>('/usage', undefined, signal);
export const restartSystem = (body?: { session_key?: string; platform?: string }) => api.post('/restart', body);
export const reloadConfig = () => api.post<{ message: string; projects_added: string[]; projects_removed: string[]; projects_updated: string[] }>('/reload');
