# Web 访问鉴权与反向代理
> 看板登录经过 Nginx 路径前缀时的跳转约定和实测问题。

- 2026-10-30：看板登录成功后进入 `/team-worker/team-worker/` 并返回 404，原因是认证处理器已经按公开前缀生成 Location，而 Nginx 的 `proxy_redirect` 又追加一次前缀。看板代理改用 `proxy_redirect off`，保留其他服务各自的重写规则；真实浏览器已验证错误 Key、成功登录、退出与重新登录均走正确路径。
