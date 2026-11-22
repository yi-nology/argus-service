import { defaultTheme } from '@vuepress/theme-default'
import { defineUserConfig } from 'vuepress'
import { viteBundler } from '@vuepress/bundler-vite'

// Argus 宣传页（VuePress 2）。base=/argus-service/ 对齐 GitHub Pages 项目站
// 路径（yi-nology.github.io/argus-service/）——仓库由 argus 改名 argus-service
// 后 Pages base 必须同步，否则 CSS/JS/图片全量 404（2026-11-22 修复）。
// 产品总站：yi-nology.github.io/argus/（工程交付中心 web/）。
export default defineUserConfig({
  lang: 'zh-CN',
  title: 'Argus',
  description: 'PR 自动审查 Bot —— 多 agent 编排，确定性裁决；真实回帖、确定性真值表、影响面视图',
  base: '/argus-service/',
  bundler: viteBundler(),
  head: [
    // rc.137 无 dark 默认分支（bootstrap 只读 localStorage+系统偏好）：无用户
    // 选择时预设 data-theme=dark（脚本须排在 theme bootstrap 之前不被覆盖，
    // 用户显式选择 light 时 bootstrap 会覆盖回来）
    ['script', {}, "(function(){localStorage.setItem('vuepress-color-scheme','dark');var e=document.documentElement;e.dataset.theme='dark';new MutationObserver(function(){if(e.dataset.theme!=='dark')e.dataset.theme='dark'}).observe(e,{attributes:true,attributeFilter:['data-theme']})})()"],
    ['link', { rel: 'icon', type: 'image/svg+xml', href: 'favicon.svg' }],
    ['meta', { name: 'theme-color', content: '#2563eb' }],
  ],
  theme: defaultTheme({
    colorMode: 'dark',
    colorModeSwitch: false,
    navbar: [
      { text: '能力', link: '/' },
      { text: '架构', link: '/architecture.html' },
      { text: 'AI 协作', link: '/skills.html' },
      { text: '部署', link: '/guide.html' },
      { text: '版本规划', link: '/roadmap.html' },
      { text: '竞品分析', link: '/vs.html' },
      { text: '产品站', link: 'https://yi-nology.github.io/argus/' },
      { text: 'README', link: 'https://github.com/yi-nology/argus-service/blob/main/README.md' },
    ],
    repo: 'yi-nology/argus-service',
    repoLabel: 'GitHub',
    docsDir: 'web',
    editLink: false,
    tip: '提示',
    warning: '注意',
    danger: '警告',
    notFound: ['页面不存在'],
    backToHome: '返回首页',
  }),
})
