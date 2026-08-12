# static-proxy

一个使用 Go 和 Gin 实现的静态资源回源缓存代理。请求的文件存在于本地时直接返回；本地缺失时从上游地址下载、保存到本地目录，然后返回给客户端。

## 功能

- 静态资源本地缓存与上游回源
- 自动创建资源对应的本地目录
- 支持跨域请求（CORS）与 `OPTIONS` 预检
- 支持部分七牛云 `imageMogr2` 风格的图片处理参数
- 自动移除回源请求中的条件缓存头，避免上游返回 `304 Not Modified`

## 环境要求

- Go 1.25.9 或更高版本

## 配置

通过环境变量配置服务：

| 变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `TARGET` | 是 | - | 上游静态资源地址，例如 `https://example.com/assets` |
| `LOCAL_DIR` | 是 | - | 本地资源存储目录，例如 `./data` |
| `PORT` | 否 | `8080` | HTTP 服务监听端口 |

## 安装与运行

```bash
go mod download

TARGET=https://example.com/assets \
LOCAL_DIR=./data \
PORT=8080 \
go run .
```

也可以先构建二进制：

```bash
go build -o static-proxy .
TARGET=https://example.com/assets LOCAL_DIR=./data ./static-proxy
```

启动后访问：

```bash
curl http://localhost:8080/images/example.jpg
```

若 `./data/images/example.jpg` 已存在，服务会直接返回该文件；否则会从 `https://example.com/assets/images/example.jpg` 下载并保存。上游未返回 HTTP 200 时，客户端会收到 404。

## 图片处理

对 JPEG、PNG 或 GIF 请求，可以在 URL 后附加 `imageMogr2` 管道参数。处理结果仅在响应时生成，不会覆盖本地原文件。

```text
http://localhost:8080/images/example.jpg?imageMogr2/thumbnail/!50p
http://localhost:8080/images/example.jpg?imageMogr2/thumbnail/300x200/quality/80
http://localhost:8080/images/example.jpg?imageMogr2/cut/600x800x100x200
```

当前支持的操作：

| 操作 | 示例 | 说明 |
| --- | --- | --- |
| `thumbnail` | `thumbnail/!50p` | 按百分比缩放 |
| `thumbnail` | `thumbnail/300x`、`thumbnail/x300`、`thumbnail/300x200` | 按宽高缩放 |
| `crop` | `crop/300x200` | 缩放到指定尺寸 |
| `cut` | `cut/300x200x10x20` | 从偏移位置裁剪指定区域 |
| `iradius` | `iradius/100` | 生成圆形图片 |
| `rradius` | `rradius/20` | 添加圆角透明区域 |
| `scrop` | `scrop/300x200` | 居中智能裁剪的简化实现（当前等同于缩放） |
| `quality` | `quality/80` | 设置 JPEG 输出质量，范围为 1–100 |

多个操作可以用 `/` 串联，例如：

```text
?imageMogr2/thumbnail/!50p/cut/200x200x0x0/quality/80
```

## 开发验证

```bash
gofmt -w main.go
go test ./...
go build ./...
```

## 注意事项

- `LOCAL_DIR` 应指向服务进程具有读写权限的目录。
- 本地已存在的资源不会自动重新从上游拉取；如需更新，请先删除对应本地文件。
- 当前缓存文件写入没有并发锁，同一路径首次出现大量并发请求时应由外部流量控制或存储层保证一致性。
