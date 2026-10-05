import { queryOptions } from "@tanstack/react-query";
import { api } from "./api";

export interface BackupConfig {
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  accessKeyId: string;
  secretAccessKey?: string;
  intervalMinutes: number;
  retain: number;
}

export interface BackupStatus {
  lastRunAt: string | null;
  lastSuccessAt: string | null;
  lastError: string;
  lastObject: string;
  lastSize: number;
}

export interface BackupSettings {
  configured: boolean;
  config: BackupConfig | null;
  status: BackupStatus;
}

export const backupQuery = queryOptions({
  queryKey: ["backups", "config"],
  queryFn: () => api<BackupSettings>("GET", "/backups/config"),
  staleTime: 60_000,
});
