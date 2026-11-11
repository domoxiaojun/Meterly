# CDN 与源站日志配置

Meterly 只统计到达源站、并在源站日志记录的请求。CDN 缓存命中但未回源的请求不在统计内。默认从 `remote_addr` 读取客户端地址；任意客户端传入的 XFF 不会直接被信任。

自动发现会识别nginx 配置及已发现 include 文件中有效的 `set_real_ip_from` / `real_ip_header`。存在 location 覆盖、无法还原 include 继承关系或缺少日志字段时，采用保守归因，在主机页检查识别方式；可显式设置可信代理。不会修改或 reload nginx/1Panel。

## 两种接入方式

- 已正确启用 nginx real-IP：日志中的 `$remote_addr` 已由 nginx 还原，Meterly 使用该字段。可信范围只应包含实际代理出口。
- nginx 未处理 real-IP：在面板采集配置中选择 `CF-Connecting-IP`、`X-Forwarded-For` 或 `X-Real-IP`，并填写可信代理 IP/CIDR。Cloudflare 可填 `cloudflare` 预设（网段快照见 agent 源码）。只有直接连接来源属于可信范围时才读取转发字段。

XFF 从链末端往前跳过可信代理，选取第一个不可信地址。IPv4/IPv6 均规范化，IPv4-mapped IPv6 与 IPv4 合并。Cloudflare 的 IPv6 字段存在且有效时优先使用。缺字段或无效链保留直接地址，并标记无法还原；不会伪装成已识别客户端。

## 推荐日志字段

在 nginx 的 `http` 上下文定义，网站的 `server` 上下文引用；路径按环境调整。下方模板只生成日志，不自动启用 real-IP 模块。

```nginx
log_format meterly escape=json
  '{"time_iso8601":"$time_iso8601",'
  '"remote_addr":"$remote_addr",'
  '"host":"$host",'
  '"status":$status,'
  '"body_bytes_sent":$body_bytes_sent,'
  '"request_length":$request_length,'
  '"http_x_forwarded_for":"$http_x_forwarded_for",'
  '"http_cf_connecting_ip":"$http_cf_connecting_ip",'
  '"http_cf_connecting_ipv6":"$http_cf_connecting_ipv6"}';

access_log /var/log/nginx/example.access.log meterly;
```

本机显式日志配置示例：

```json
{
  "collectors": {
    "site": {
      "enabled": true,
      "sources": [{"path": "/var/log/nginx/example.access.log", "site": "example.com"}],
      "allow_dirs": ["/var/log/nginx"],
      "trusted_proxies": ["cloudflare"],
      "ip_header": "CF-Connecting-IP"
    }
  }
}
```

将此片段合入现有 agent 配置，不要覆盖中心地址、token_file 等字段。本机显式设置优先于面板。日志若没有请求长度，上行显示“不可用”，已知流量合计只加已知字节。

代理网段和具体部署方式请核对 [nginx real-IP 文档](https://nginx.org/en/docs/http/ngx_http_realip_module.html) 与 [Cloudflare 原始 IP 说明](https://developers.cloudflare.com/support/troubleshooting/restoring-visitor-ips/restoring-original-visitor-ips/)。Cloudflare 网段来自 [IPv4 清单](https://www.cloudflare.com/ips-v4) 和 [IPv6 清单](https://www.cloudflare.com/ips-v6)。
