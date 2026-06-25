---
name: common-rejection-reasons
description: Common Google AdSense and quality review rejection reasons with evidence-oriented remediation strategies.
tags:
  - google
  - adsense
  - rejection
  - common-reasons
---

# Common Rejection Reasons

本技能用于把拒审原因映射为可验证风险。不要把这里的启发式当成 Google 官方硬规则。

## 1. Low Value Content

表现：

- 页面可以访问，但主要内容空洞、模板化或重复。
- 核心页面只有 UI、列表、表单或按钮，没有足够发布者内容说明价值。
- FAQ、Use Cases、How it works 在大量页面复用。
- 多语言页内容缩水或机器翻译痕迹明显。
- 站点之间复用同一套内容、功能、服务说明或页面模板。

修复：

- 为核心页面补主题相关的独特模块：示例、边界、错误排查、隐私处理、真实工作流、替代方案比较。
- 合并、noindex 或删除低价值页面。
- 对跨站重复内容做差异化定位或选主站。
- 收缩 sitemap，只暴露有价值页面。

注意：

- 不存在“每页达到某个固定字数就合规”的可靠规则。
- 内容长度只是启发式，独特价值和用户任务完成度更重要。

## 2. Duplicate / Scaled Content

表现：

- 批量生成 `[topic]`、`[lang]`、`[city]`、`[category]` 等页面，但页面差异主要是标题和关键词。
- 帮助/说明页和主页面互相复制。
- 两个域名发布相同内容、功能、服务说明、文档或 FAQ。

修复：

- 给每个核心页面明确独特任务。
- 合并重复页面。
- 使用 canonical/noindex 管理重复或弱页面。
- 两个站点建立不同品牌定位、页面结构和内容重点。

## 3. Inventory Value Problems

表现：

- 广告出现在薄页、404、搜索结果、纯导航或空状态页面。
- 广告数量或视觉权重大于发布者内容。
- 广告靠近下载、复制、提交等核心按钮造成误点风险。

修复：

- 关闭低价值页面广告。
- 在核心内容足够明确之后再放广告。
- 广告与核心操作保持清晰分隔。

## 4. Missing Trust Signals

表现：

- 没有 About / Contact / Privacy / Terms。
- Contact 页面没有可用联系方式。
- Privacy 未披露广告、Cookie、Analytics、第三方服务。
- 页面声称完全本地处理，但又加载第三方脚本且没有解释。

修复：

- 从首页和页脚链接到信任页。
- 提供真实可用联系渠道。
- 补齐广告、Cookie、Analytics 和用户数据处理披露。
- 不编造公司、团队、地址或资质。

## 5. Technical Discovery Problems

表现：

- robots/sitemap/ads.txt 缺失或指向错误域名。
- sitemap 包含 404 或低价值页面。
- canonical/hreflang 错误。
- 导航断链。

修复：

- 修复 robots、sitemap、ads.txt。
- 重新生成 sitemap。
- 修复断链与 canonical。
- 多语言页面建立一致 hreflang 策略。

## 6. Deceptive Or Broken UX

表现：

- 假下载、假复制、误导按钮。
- 弹窗/广告遮挡核心内容。
- 移动端文本重叠、按钮不可点击。
- JS 加载失败时主要内容近乎为空。

修复：

- 移除误导 UI。
- 保证移动端核心任务可完成。
- 给空状态、错误状态、加载状态提供清楚说明。

## 快速门控

- [ ] P0 阻塞项为 0。
- [ ] 核心页面具备独特用户价值。
- [ ] 没有大量重复/模板页暴露给索引。
- [ ] 低价值页面不展示广告。
- [ ] 信任页完整、可达、真实。
- [ ] robots、sitemap、ads.txt 正确。
- [ ] 导航无关键断链。
- [ ] 移动端可完成核心任务。
