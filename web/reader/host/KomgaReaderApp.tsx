/*
 * Copyright (C) Contributors to the Suwayomi project
 * Copyright (C) fake-komga-115 contributors
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

import CssBaseline from '@mui/material/CssBaseline';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import IconButton from '@mui/material/IconButton';
import InputAdornment from '@mui/material/InputAdornment';
import Stack from '@mui/material/Stack';
import TextField from '@mui/material/TextField';
import Typography from '@mui/material/Typography';
import VisibilityIcon from '@mui/icons-material/Visibility';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import type { PropsWithChildren } from 'react';
import React, { useEffect, useRef, useState } from 'react';
import { Route, Routes, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { AwaitableComponent } from 'awaitable-component';
import { loadable } from 'react-lazily/loadable';
import { AppContext } from '@/base/contexts/AppContext.tsx';
import { ErrorBoundary } from '@/base/components/feedback/ErrorBoundary.tsx';
import { LoadingPlaceholder } from '@/base/components/feedback/LoadingPlaceholder.tsx';
import { ReactRouter } from '@/lib/react-router/ReactRouter.ts';
import { requestManager } from '@/lib/requests/RequestManager.ts';
import { useNavBarContext } from '@/features/navigation-bar/NavbarContext.tsx';
import { ReaderResumeMode } from '@/features/reader/Reader.types.ts';
import { KomgaHostBridge, type ArchivePasswordPromptRequest } from '@/host/KomgaHostBridge.ts';

const blackLoadingFallback = {
    fallback: (
        <Box sx={{ height: '100vh', width: '100vw', display: 'grid', placeItems: 'center', bgcolor: '#000' }}>
            <LoadingPlaceholder />
        </Box>
    ),
};

const { Reader } = loadable(() => import('@/features/reader/screens/Reader.tsx'), blackLoadingFallback);

const ReactRouterSetter = () => {
    const navigate = useNavigate();
    useEffect(() => {
        ReactRouter.setNavigateFn(navigate);
    }, [navigate]);
    return null;
};

const InitializeGuard = ({ children }: PropsWithChildren) => {
    const [ready, setReady] = useState(false);

    useEffect(() => {
        Promise.allSettled([
            requestManager.getGlobalMeta().response,
            requestManager.getServerSettings().response,
        ]).finally(() => setReady(true));
    }, []);

    if (!ready) {
        return (
            <Box sx={{ height: '100vh', width: '100vw', display: 'grid', placeItems: 'center', bgcolor: '#000' }}>
                <LoadingPlaceholder />
            </Box>
        );
    }
    return children;
};

const ReaderOverlayHost = () => {
    const { override } = useNavBarContext();
    if (!override.status) return null;
    return override.value;
};

const KomgaEntryRedirect: React.FC = () => {
    const { seriesId = '', bookId = '' } = useParams<{ seriesId: string; bookId: string }>();
    const [searchParams] = useSearchParams();
    const navigate = useNavigate();
    const [errText, setErrText] = useState<string | null>(null);

    useEffect(() => {
        let cancelled = false;
        const pageRaw = searchParams.get('page');
        const pageNum = pageRaw ? parseInt(pageRaw, 10) : undefined;
        KomgaHostBridge.prepareEntry(
            seriesId,
            bookId,
            pageNum && !Number.isNaN(pageNum) && pageNum >= 1 ? pageNum : undefined,
        )
            .then(({ mangaId, sourceOrder }) => {
                if (cancelled) return;
                navigate(`/manga/${mangaId}/chapter/${sourceOrder}`, {
                    replace: true,
                    state: {
                        resumeMode: ReaderResumeMode.LAST_READ,
                        updateInitialChapter: true,
                    },
                });
            })
            .catch((e) => {
                if (!cancelled) {
                    setErrText(e instanceof Error ? e.message : String(e));
                }
            });
        return () => {
            cancelled = true;
        };
    }, [seriesId, bookId, searchParams, navigate]);

    if (errText) {
        return (
            <Stack
                spacing={2}
                sx={{
                    height: '100vh',
                    width: '100vw',
                    alignItems: 'center',
                    justifyContent: 'center',
                    bgcolor: '#0b0d12',
                    color: '#fff',
                    p: 3,
                }}
            >
                <Typography variant="h6">无法打开阅读器</Typography>
                <Typography variant="body2" sx={{ color: 'rgba(255,255,255,0.7)' }}>
                    {errText}
                </Typography>
                <Button variant="contained" onClick={() => window.location.assign(seriesId ? `/series/${encodeURIComponent(seriesId)}` : '/')}>
                    返回书架
                </Button>
            </Stack>
        );
    }

    return (
        <Box sx={{ height: '100vh', width: '100vw', display: 'grid', placeItems: 'center', bgcolor: '#000' }}>
            <LoadingPlaceholder />
        </Box>
    );
};

const ExitToSeriesPage: React.FC = () => {
    const { id } = useParams<{ id: string }>();
    useEffect(() => {
        KomgaHostBridge.flushAllProgress();
        const mangaId = Number(id);
        const seriesId = !Number.isNaN(mangaId) ? KomgaHostBridge.getSeriesIdForManga(mangaId) : undefined;
        window.location.replace(seriesId ? `/series/${encodeURIComponent(seriesId)}` : '/');
    }, [id]);
    return null;
};

const ExitToHome: React.FC = () => {
    useEffect(() => {
        KomgaHostBridge.flushAllProgress();
        window.location.replace('/');
    }, []);
    return null;
};

const ArchivePasswordPromptHost: React.FC = () => {
    const [req, setReq] = useState<ArchivePasswordPromptRequest | null>(null);
    const [password, setPassword] = useState('');
    const [showPassword, setShowPassword] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [errorText, setErrorText] = useState('');
    const resolverRef = useRef<((unlocked: boolean) => void) | null>(null);

    useEffect(() => {
        KomgaHostBridge.setPasswordPromptHandler(
            (promptReq) =>
                new Promise<boolean>((resolve) => {
                    resolverRef.current = resolve;
                    setPassword('');
                    setShowPassword(false);
                    setSubmitting(false);
                    setErrorText(promptReq.invalid ? '之前保存的密码未能解压此文件，请重新输入密码。' : '');
                    setReq(promptReq);
                }),
        );
        return () => {
            KomgaHostBridge.setPasswordPromptHandler(null);
        };
    }, []);

    const handleCancel = () => {
        if (submitting) return;
        const resolve = resolverRef.current;
        const currentReq = req;
        resolverRef.current = null;
        setReq(null);
        resolve?.(false);
        if (currentReq?.seriesId) {
            window.location.assign(`/series/${encodeURIComponent(currentReq.seriesId)}`);
        }
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!req || submitting) return;
        if (!password) {
            setErrorText('请输入解压密码');
            return;
        }
        setSubmitting(true);
        setErrorText('');
        try {
            await KomgaHostBridge.unlockBookArchive(req.bookId, password);
            const resolve = resolverRef.current;
            resolverRef.current = null;
            setReq(null);
            resolve?.(true);
        } catch (err) {
            setErrorText(err instanceof Error ? err.message : '解压密码验证失败，请重试');
        } finally {
            setSubmitting(false);
        }
    };

    return (
        <Dialog
            open={Boolean(req)}
            onClose={handleCancel}
            fullWidth
            maxWidth="xs"
            slotProps={{
                paper: {
                    sx: {
                        bgcolor: '#14171f',
                        color: '#f8fafc',
                        border: '1px solid rgba(255,255,255,0.12)',
                        borderRadius: 2.5,
                    },
                },
            }}
        >
            <form onSubmit={handleSubmit}>
                <DialogTitle sx={{ pb: 1, fontSize: '1.05rem', fontWeight: 600 }}>
                    压缩包已加密，请输入解压密码
                </DialogTitle>
                <DialogContent sx={{ pt: '6px !important' }}>
                    {req && (
                        <Typography variant="body2" sx={{ color: 'rgba(226,232,240,0.8)', mb: 1.5, wordBreak: 'break-all' }}>
                            {req.seriesTitle ? `${req.seriesTitle} · ` : ''}
                            {req.bookName}
                        </Typography>
                    )}
                    <TextField
                        autoFocus
                        fullWidth
                        size="small"
                        type={showPassword ? 'text' : 'password'}
                        label="解压密码"
                        placeholder="输入此压缩包的解压密码"
                        value={password}
                        onChange={(e) => setPassword(e.target.value)}
                        disabled={submitting}
                        error={Boolean(errorText)}
                        helperText={errorText || '验证通过后将自动记住并与该漫画绑定，同文件夹其它加密文件会自动尝试复用。'}
                        slotProps={{
                            input: {
                                endAdornment: (
                                    <InputAdornment position="end">
                                        <IconButton
                                            size="small"
                                            edge="end"
                                            aria-label={showPassword ? '隐藏密码' : '显示密码'}
                                            onClick={() => setShowPassword((v) => !v)}
                                            sx={{ color: 'rgba(226,232,240,0.75)' }}
                                        >
                                            {showPassword ? <VisibilityOffIcon fontSize="small" /> : <VisibilityIcon fontSize="small" />}
                                        </IconButton>
                                    </InputAdornment>
                                ),
                            },
                        }}
                    />
                </DialogContent>
                <DialogActions sx={{ px: 3, pb: 2 }}>
                    <Button onClick={handleCancel} disabled={submitting} color="inherit">
                        返回书架
                    </Button>
                    <Button type="submit" variant="contained" disabled={submitting}>
                        {submitting ? '正在验证…' : '解锁并记住'}
                    </Button>
                </DialogActions>
            </form>
        </Dialog>
    );
};

export const App: React.FC = () => (
    <AppContext>
        <AwaitableComponent.Root />
        <ArchivePasswordPromptHost />
        <ReactRouterSetter />
        <CssBaseline enableColorScheme />
        <InitializeGuard>
            <Box sx={{ display: 'flex' }}>
                <Box sx={{ flexShrink: 0, position: 'relative', height: '100vh' }}>
                    <ReaderOverlayHost />
                </Box>
                <ErrorBoundary>
                    <Routes>
                        <Route path="/series/:seriesId/book/:bookId/*" element={<KomgaEntryRedirect />} />
                        <Route path="/manga/:mangaId/chapter/:chapterSourceOrder/*" element={<Reader />} />
                        <Route path="/manga/:id/*" element={<ExitToSeriesPage />} />
                        <Route path="*" element={<ExitToHome />} />
                    </Routes>
                </ErrorBoundary>
            </Box>
        </InitializeGuard>
    </AppContext>
);
