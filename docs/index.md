---
layout: home

hero:
  name: KeelUni
  text: 一站式交易终端
  tagline: 一副龙骨，承载所有市场。自托管、单人使用，用一个终端交易你连接的每个交易所。
  image:
    src: /logo.svg
    alt: KeelUni
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/quick-start
    - theme: alt
      text: 架构设计
      link: /architecture/
    - theme: alt
      text: API 参考
      link: /api/

features:
  - title: 一个终端，所有交易所
    details: Binance、OKX、Bybit、Bitget、Gate、Hyperliquid、Lighter、Kraken、Deribit 的永续与现货，统一的标识、单位、规则与错误分类。
  - title: 账户与市场解耦
    details: 标的永远是订单簿所在交易所的；账户决定"用谁的钱、走哪条通道"。Gate CrossEx 这种跨所保证金账户是一种账户，而不是一个页面。
  - title: 下单安全优先
    details: 幂等只靠交易所 clientOrderId；结果未知时只查不重下；交易所是唯一事实来源，gateway 不保存订单账本。
  - title: 对冲作为一笔仓位
    details: hedger 把两条腿当作一个仓位开、管、平；一边挂限价一边市价对冲，失衡时自动修复或交给你决定，并与交易所持仓持续对账。
  - title: 统一的资金费率
    details: 所有永续的资金费率统一为"单次结算比例 + 周期 + 年化"，常驻采集、跨交易所可比。
  - title: 中英双语
    details: 界面默认中文、可切换英文；后端消息以编码 + 参数传递，按阅读者的语言渲染。
---
