# 代码审查知识库

## Go 安全最佳实践

Go 语言常见安全问题和最佳实践：

1. **SQL 注入**：始终使用参数化查询（`?` 占位符），不要拼接 SQL 字符串
2. **模板注入**：`html/template` 自动转义，`text/template` 不转义，用于 HTML 输出时用前者
3. **路径遍历**：使用 `filepath.Clean()` 和 `filepath.Join()` 防止路径遍历攻击
4. **加密**：使用 `crypto/rand` 而非 `math/rand` 生成随机数，使用 `crypto/sha256` 而非 `crypto/md5`
5. **敏感信息**：不要在代码中硬编码密钥，使用环境变量或密钥管理服务
6. **CSRF**：Web 应用应使用 CSRF token

## Go 性能最佳实践

1. **字符串拼接**：大量拼接使用 `strings.Builder` 而非 `+=`
2. **slice 预分配**：已知大小时预分配 `make([]T, 0, capacity)`
3. **sync.Pool**：高频创建销毁的对象使用 `sync.Pool` 复用
4. **channel 大小**：非缓冲 channel 可能导致 goroutine 阻塞，考虑使用缓冲 channel
5. **defer**：在循环中使用 defer 会导致资源延迟释放，应在循环外用函数包裹
6. **map 预分配**：已知大小时 `make(map[K]V, capacity)` 减少 rehash
