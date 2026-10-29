# 看板终端显示问题
> Ghostty 终端的浏览器光标、自动高度与网格滚动的排查结论。

- 2026-10-29：点击终端后左上角出现插入光标，原因是 ghostty-web 的 `.term-surface` 使用 `contenteditable`，浏览器绘制了原生 caret；设置 `caret-color: transparent` 隐藏它，Canvas 内的终端光标仍由 Ghostty 绘制。已在运行中的三个真实终端验证，聚焦后计算样式为透明。
- 2026-10-29：多个终端的可视高度缩到 228px，且外层没有滚动条；`AgentTerminal.layout()` 会按缩放后的 Canvas 写入较小高度，Grid 的自动行也会压缩卡片。仅将 `.term-host` 的最小高度设为 380px 仍不能保证外层滚动，还需 `.terms { grid-auto-rows: max-content; }`。运行中实测三个终端均保留 380px，桌面列表可视高度 794px、内容高度 1043px；全屏保留较小的最小高度，离线卡片保持原有紧凑布局。相关实现见 `internal/board/web/src/style.css` 与 `internal/board/web/src/terminals.js`。

- 2026-10-29：替换运行中看板的 CSS 内容后，页面入口仍引用旧的哈希文件名，不能保证已缓存该资源的浏览器收到修改。热更新时一并切换完整 Vite 入口和 assets，使 HTML 引用新哈希；本次已在用户提供的 URL 上验证加载 `index-XwcpHfls.css`。新页面实测三个终端均为 380px，外层可视高度 767px、内容高度 1017px。

- 2026-10-29：接管终端后输入法组词位置偏离终端光标，原因是 ghostty-web 0.4.0 聚焦 contenteditable 容器，隐藏 textarea 固定在 `(0, 0)`。接管时将焦点转到 textarea，用终端光标行列和 renderer 字体度量更新其坐标，并在组词期间显示预编辑文本。`internal/board/web/tests/terminal_input_test.py` 通过真实 Ghostty 与 Chromium CDP 验证行列变化、字体缩放、2 倍像素比、英文输入、中文确认/取消及粘贴；操作系统原生候选窗口的位置仍需本机输入法确认。
- 2026-10-29：显示 IME 预编辑 textarea 后，页面通用 `:focus-visible` 规则会给它画出方框；浏览器组合输入测试复现 `outlineStyle: solid`，给 `.term-surface textarea` 单独设置 `outline: none` 后变为 `none`，中文确认、取消和粘贴测试仍通过。终端卡片继续显示自身的选中及接管状态。
