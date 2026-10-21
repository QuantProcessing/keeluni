import { defineConfig } from "vitepress";

// KeelUni documentation, published to GitHub Pages by .github/workflows/docs.yml
// at https://quantprocessing.github.io/keeluni/.
export default defineConfig({
  lang: "zh-CN",
  title: "KeelUni",
  description: "KeelUni：一站式交易终端的设计与实现文档",
  base: "/keeluni/",
  cleanUrls: true,
  lastUpdated: true,
  // Source material kept for reference, not part of the site.
  srcExclude: ["**/node_modules/**"],
  head: [["link", { rel: "icon", type: "image/svg+xml", href: "/keeluni/logo.svg" }]],
  markdown: { lineNumbers: false },
  themeConfig: {
    logo: "/logo.svg",
    siteTitle: "KeelUni",
    nav: [
      { text: "指南", link: "/guide/", activeMatch: "/guide/" },
      { text: "架构", link: "/architecture/", activeMatch: "/architecture/" },
      { text: "模块", link: "/exchange/", activeMatch: "/(exchange|gateway|hedger|optioner|spreader|web)/" },
      { text: "API", link: "/api/", activeMatch: "/api/" },
      { text: "开发", link: "/dev/", activeMatch: "/dev/" },
    ],
    sidebar: [
      {
        text: "指南",
        items: [
          { text: "KeelUni 是什么", link: "/guide/" },
          { text: "快速开始", link: "/guide/quick-start" },
          { text: "部署与运维", link: "/guide/deploy" },
          { text: "使用终端", link: "/guide/using" },
          { text: "安全", link: "/guide/security" },
        ],
      },
      {
        text: "架构",
        items: [
          { text: "总览与分层", link: "/architecture/" },
          { text: "设计决策", link: "/architecture/decisions" },
          { text: "标识与路由", link: "/architecture/identity" },
          { text: "产品类别与期权", link: "/architecture/products" },
          { text: "订单生命周期", link: "/architecture/orders" },
          { text: "资金费率", link: "/architecture/funding" },
          { text: "限流与连接", link: "/architecture/rate-limits" },
        ],
      },
      {
        text: "模块",
        items: [
          { text: "sdk 与 exchange", link: "/exchange/" },
          { text: "交易所差异", link: "/exchange/venues" },
          { text: "Gate CrossEx", link: "/exchange/crossex" },
          { text: "附录：REST 操作矩阵（英文）", link: "/exchange/rest-operations" },
          { text: "附录：WebSocket 操作矩阵（英文）", link: "/exchange/ws-operations" },
          { text: "gateway", link: "/gateway/" },
          { text: "trader", link: "/gateway/trader" },
          { text: "hedger 对冲", link: "/hedger/" },
          { text: "hedger 执行细节", link: "/hedger/execution" },
          { text: "optioner 期权", link: "/optioner/" },
          { text: "spreader 价差套利（设计）", link: "/spreader/design" },
          { text: "前端", link: "/web/" },
          { text: "设计规范", link: "/web/design" },
        ],
      },
      {
        text: "API",
        items: [
          { text: "概览与约定", link: "/api/" },
          { text: "gateway REST", link: "/api/gateway-rest" },
          { text: "gateway WebSocket", link: "/api/gateway-ws" },
          { text: "trader", link: "/api/trader" },
          { text: "hedger", link: "/api/hedger" },
          { text: "optioner", link: "/api/optioner" },
        ],
      },
      {
        text: "开发",
        items: [
          { text: "开发与测试", link: "/dev/" },
          { text: "接入新交易所", link: "/dev/new-venue" },
          { text: "国际化", link: "/dev/i18n" },
        ],
      },
    ],
    socialLinks: [{ icon: "github", link: "https://github.com/QuantProcessing/keeluni" }],
    search: {
      provider: "local",
      options: {
        translations: {
          button: { buttonText: "搜索", buttonAriaLabel: "搜索" },
          modal: { noResultsText: "没有结果", resetButtonTitle: "清除", footer: { selectText: "选择", navigateText: "切换", closeText: "关闭" } },
        },
      },
    },
    editLink: { pattern: "https://github.com/QuantProcessing/keeluni/edit/master/docs/:path", text: "在 GitHub 上编辑此页" },
    outline: { level: [2, 3], label: "本页目录" },
    docFooter: { prev: "上一页", next: "下一页" },
    lastUpdated: { text: "最后更新" },
    returnToTopLabel: "回到顶部",
    sidebarMenuLabel: "菜单",
    darkModeSwitchLabel: "外观",
    footer: { message: "KeelUni · 一站式交易终端", copyright: "QuantProcessing" },
  },
});
