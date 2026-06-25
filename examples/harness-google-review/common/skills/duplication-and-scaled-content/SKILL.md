---
name: duplication-and-scaled-content
description: Detect internal duplication, cross-site reuse, and scaled content patterns across generic Google/AdSense review sites.
tags:
  - duplication
  - scaled-content
  - content-risk
---

# Duplication And Scaled Content

本技能用于识别“看起来像批量生成”的内容风险。

## 高风险模式

- 多个页面只有标题、主题名、关键词不同。
- FAQ、benefits、use cases、how it works 跨页面复用。
- 多语言页只是机器翻译且没有本地化价值。
- 帮助/说明页和主页面互相复制。
- 两个域名发布同一套内容、功能、服务说明或文档。
- sitemap 暴露大量弱页面。

## 证据类型

- `duplicatePairs`：站内高相似页面。
- `crossSiteSimilarPages`：跨站高相似页面。
- 源码中动态路由和批量生成逻辑。
- 重复 meta title/description。
- 重复 FAQ 或数据文件。
- TODO/placeholder/模板标记。

## 修复策略

按优先级选择：

1. 差异化：给页面增加主题相关的独特价值，改变结构和内容重点。
2. 合并：把多个弱页面合成一个强页面。
3. noindex：暂时不让弱页面进入索引和审核面。
4. canonical：重复页面指向主页面。
5. 删除：无法维护价值的页面从 sitemap 和导航中移除。

## 双站策略

如果两个站点都申请 AdSense，必须明确：

- 哪个站是主申请站。
- 两站是否有不同受众、语言、功能或内容策略。
- 相同主题、功能、服务或内容页如何差异化。
- 哪些页面应只保留在一个域名。

不要同时把两个高度相似的站点提交审核。

## 判断原则

- 相似度分数只是线索，不是最终结论。
- 模板复用本身不是问题；模板复用加上缺少独特价值才是问题。
- 审核风险来自规模化弱内容，而不是单个短页面。
