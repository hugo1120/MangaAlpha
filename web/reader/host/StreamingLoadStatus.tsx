/*
 * Copyright (C) fake-komga-115 contributors
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

import Box from '@mui/material/Box';
import Chip from '@mui/material/Chip';
import CircularProgress from '@mui/material/CircularProgress';
import LinearProgress from '@mui/material/LinearProgress';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import React, { useEffect, useState } from 'react';
import { ReaderBackgroundColor } from '@/features/reader/Reader.types.ts';
import { useReaderSettingsStore } from '@/features/reader/stores/ReaderStore.ts';

export interface BookLoadProgress {
    active: boolean;
    bookId: string;
    stage: string;
    message?: string;
    provider?: string;
    pageNumber?: number;
    bytesLoaded?: number;
    bytesTotal?: number;
    pagesFound?: number;
    elapsedMs?: number;
}

const PAGE_URL_RE = /\/api\/v1\/books\/([^/?#]+)\/pages\/(\d+)(?:[/?#]|$)/;

export function parseKomgaPageUrl(src: string): { bookId: string; pageNumber: number } | null {
    if (!src) return null;
    const match = PAGE_URL_RE.exec(src);
    if (!match) return null;
    const pageNumber = parseInt(match[2], 10);
    if (Number.isNaN(pageNumber) || pageNumber < 1) return null;
    try {
        return { bookId: decodeURIComponent(match[1]), pageNumber };
    } catch {
        return { bookId: match[1], pageNumber };
    }
}

function formatBytes(bytes?: number): string {
    const n = Number(bytes || 0);
    if (n <= 0) return '0 B';
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(2)} MiB`;
    return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
}

function providerLabel(provider?: string): string {
    switch (provider) {
        case '115':
            return '115 网盘串流';
        case 'webdav':
            return 'WebDAV 串流';
        case 'local':
            return '本地归档';
        case 'union':
            return '联合书库';
        default:
            return '';
    }
}

function stageFallbackLabel(stage: string | undefined, mode: 'index' | 'page'): string {
    switch (stage) {
        case 'indexing':
            return '正在解析归档目录索引…';
        case 'downloading':
            return '正在从网络源拉取数据分块…';
        case 'decompressing':
            return '正在解压漫画页面…';
        default:
            return mode === 'index' ? '正在打开漫画章节…' : '正在缓冲图像数据…';
    }
}

function useBookLoadStatus(
    bookId: string | undefined,
    pageNumber: number,
    active: boolean,
    delayMs: number,
): { status: BookLoadProgress | null; elapsedMs: number; detailVisible: boolean } {
    const [status, setStatus] = useState<BookLoadProgress | null>(null);
    const [elapsedMs, setElapsedMs] = useState(0);
    const [detailVisible, setDetailVisible] = useState(delayMs === 0);

    useEffect(() => {
        if (!active || !bookId) {
            setStatus(null);
            setElapsedMs(0);
            setDetailVisible(delayMs === 0);
            return () => {};
        }

        const startedAt = Date.now();
        let cancelled = false;
        let pollTimer: ReturnType<typeof setTimeout> | null = null;
        let currentController: AbortController | null = null;

        const endpoint =
            pageNumber > 0
                ? `/api/v1/books/${encodeURIComponent(bookId)}/load-status?page=${pageNumber}`
                : `/api/v1/books/${encodeURIComponent(bookId)}/load-status`;

        const pollOnce = async () => {
            if (cancelled) return;
            setDetailVisible(true);
            setElapsedMs(Date.now() - startedAt);
            currentController = new AbortController();
            try {
                const resp = await fetch(endpoint, {
                    credentials: 'same-origin',
                    headers: { 'X-FK115-UI': '1' },
                    signal: currentController.signal,
                });
                if (resp.ok && !cancelled) {
                    const data = (await resp.json()) as BookLoadProgress;
                    setStatus(data);
                }
            } catch {
                // Ignore transient abort/network errors during polling
            }
            if (!cancelled) {
                setElapsedMs(Date.now() - startedAt);
                pollTimer = setTimeout(pollOnce, 350);
            }
        };

        const startTimer = setTimeout(() => {
            pollOnce().catch(() => {});
        }, delayMs);

        return () => {
            cancelled = true;
            clearTimeout(startTimer);
            if (pollTimer) clearTimeout(pollTimer);
            if (currentController) currentController.abort();
        };
    }, [bookId, pageNumber, active, delayMs]);

    return { status, elapsedMs, detailVisible };
}

function computeProgressPercent(status: BookLoadProgress | null): number | null {
    if (!status || !status.active) return null;
    const loaded = Number(status.bytesLoaded || 0);
    const total = Number(status.bytesTotal || 0);
    if (loaded <= 0 || total <= 0) return null;
    return Math.max(1, Math.min(99, Math.round((loaded / total) * 100)));
}

function useLoadTextColors(): { primary: string; secondary: string; trackBg: string; chipBg: string; chipColor: string; chipBorder: string } {
    const backgroundColor = useReaderSettingsStore('backgroundColor');
    const isLightCanvas =
        backgroundColor === ReaderBackgroundColor.WHITE || backgroundColor === ReaderBackgroundColor.GRAY;
    if (isLightCanvas) {
        return {
            primary: '#111827',
            secondary: 'rgba(17, 24, 39, 0.72)',
            trackBg: 'rgba(17, 24, 39, 0.14)',
            chipBg: 'rgba(79, 70, 229, 0.12)',
            chipColor: '#3730a3',
            chipBorder: 'rgba(79, 70, 229, 0.28)',
        };
    }
    return {
        primary: '#f8fafc',
        secondary: 'rgba(226, 232, 240, 0.74)',
        trackBg: 'rgba(255, 255, 255, 0.14)',
        chipBg: 'rgba(99, 102, 241, 0.22)',
        chipColor: '#c7d2fe',
        chipBorder: 'rgba(129, 140, 248, 0.35)',
    };
}

export const BookIndexProgress: React.FC<{ bookId?: string }> = ({ bookId }) => {
    const { status, elapsedMs } = useBookLoadStatus(bookId, 0, Boolean(bookId), 120);
    const colors = useLoadTextColors();
    const pct = computeProgressPercent(status);
    const provText = providerLabel(status?.provider);
    const stageText = (status?.active && status.message) || stageFallbackLabel(status?.stage, 'index');
    const loaded = Number(status?.bytesLoaded || 0);
    const total = Number(status?.bytesTotal || 0);
    const pagesFound = Number(status?.pagesFound || 0);
    const shownElapsed = Math.max(elapsedMs, Number(status?.elapsedMs || 0));

    const metaParts: string[] = [];
    if (loaded > 0 && total > 0 && pct !== null) {
        metaParts.push(`${formatBytes(loaded)} / ${formatBytes(total)} (${pct}%)`);
    } else if (loaded > 0) {
        metaParts.push(`已读取 ${formatBytes(loaded)}`);
    }
    if (pagesFound > 0) {
        metaParts.push(`已发现 ${pagesFound} 页`);
    }
    if (shownElapsed >= 1200) {
        metaParts.push(`已耗时 ${(shownElapsed / 1000).toFixed(1)}s`);
    }

    return (
        <Stack
            spacing={1.5}
            alignItems="center"
            sx={{
                width: 'min(320px, 84vw)',
                px: 2,
                py: 2,
                bgcolor: 'transparent',
                color: colors.primary,
                textAlign: 'center',
            }}
        >
            <CircularProgress
                thickness={4.5}
                size={38}
                variant={pct !== null ? 'determinate' : 'indeterminate'}
                value={pct ?? undefined}
            />
            {provText && (
                <Chip
                    size="small"
                    label={provText}
                    sx={{
                        height: 22,
                        fontSize: '0.72rem',
                        bgcolor: colors.chipBg,
                        color: colors.chipColor,
                        border: `1px solid ${colors.chipBorder}`,
                    }}
                />
            )}
            <Typography variant="body2" sx={{ fontWeight: 500, color: colors.primary, lineHeight: 1.45 }}>
                {stageText}
            </Typography>
            <Box sx={{ width: '100%' }}>
                <LinearProgress
                    variant={pct !== null ? 'determinate' : 'indeterminate'}
                    value={pct ?? undefined}
                    sx={{
                        height: 6,
                        borderRadius: 99,
                        bgcolor: colors.trackBg,
                    }}
                />
            </Box>
            {metaParts.length > 0 && (
                <Typography variant="caption" sx={{ color: colors.secondary }}>
                    {metaParts.join(' · ')}
                </Typography>
            )}
        </Stack>
    );
};

export const PageLoadProgress: React.FC<{ src: string; small?: boolean }> = ({ src, small }) => {
    const parsed = parseKomgaPageUrl(src);
    const shouldTrack = !small && parsed !== null;
    const colors = useLoadTextColors();
    const { status, elapsedMs, detailVisible } = useBookLoadStatus(
        parsed?.bookId,
        parsed?.pageNumber ?? 0,
        shouldTrack,
        240,
    );

    if (!shouldTrack || !detailVisible) {
        return <CircularProgress thickness={5} />;
    }

    const pct = computeProgressPercent(status);
    const provText = providerLabel(status?.provider);
    const loaded = Number(status?.bytesLoaded || 0);
    const total = Number(status?.bytesTotal || 0);
    const shownElapsed = Math.max(elapsedMs, Number(status?.elapsedMs || 0));

    let label = `第 ${parsed.pageNumber} 页 · 正在接收图像…`;
    if (status?.active) {
        label = `第 ${parsed.pageNumber} 页 · ${status.message || stageFallbackLabel(status.stage, 'page')}`;
    }

    const subParts: string[] = [];
    if (provText) subParts.push(provText);
    if (loaded > 0 && total > 0 && pct !== null) {
        subParts.push(`${formatBytes(loaded)} / ${formatBytes(total)} (${pct}%)`);
    } else if (loaded > 0) {
        subParts.push(`已缓冲 ${formatBytes(loaded)}`);
    }
    if (shownElapsed >= 1200) {
        subParts.push(`${(shownElapsed / 1000).toFixed(1)}s`);
    }

    return (
        <Stack
            spacing={1}
            alignItems="center"
            sx={{
                px: 2,
                py: 1.5,
                bgcolor: 'transparent',
                color: colors.primary,
                maxWidth: 'min(280px, 82vw)',
                textAlign: 'center',
            }}
        >
            <CircularProgress
                thickness={4.5}
                size={34}
                variant={pct !== null ? 'determinate' : 'indeterminate'}
                value={pct ?? undefined}
            />
            <Typography variant="caption" sx={{ color: colors.primary, fontWeight: 500, lineHeight: 1.4 }}>
                {label}
            </Typography>
            <Box sx={{ width: 170, maxWidth: '100%' }}>
                <LinearProgress
                    variant={pct !== null ? 'determinate' : 'indeterminate'}
                    value={pct ?? undefined}
                    sx={{
                        height: 5,
                        borderRadius: 99,
                        bgcolor: colors.trackBg,
                    }}
                />
            </Box>
            {subParts.length > 0 && (
                <Typography variant="caption" sx={{ color: colors.secondary, fontSize: '0.7rem' }}>
                    {subParts.join(' · ')}
                </Typography>
            )}
        </Stack>
    );
};
