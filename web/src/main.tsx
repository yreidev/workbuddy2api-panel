// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { domMax, LazyMotion, MotionConfig } from 'motion/react'
import './index.css'
import { printConsoleNotice } from './lib/notice'
import { queryClient } from './lib/queries'
import { router } from './router'

printConsoleNotice()

// 发了新版本后，旧标签页去加载已经不存在的分包会失败：刷新一下拿到新版本
window.addEventListener('vite:preloadError', (e) => {
  e.preventDefault()
  location.reload()
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MotionConfig reducedMotion="user">
      <LazyMotion features={domMax} strict>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </LazyMotion>
    </MotionConfig>
  </StrictMode>,
)
