# 邮箱账号导入

在自动化页面点击「导入邮箱账号」，选择登录方式，再选择 TXT 文件或粘贴文本。文件内容会填入账号列表，只有点击「预览与校验」才会发送到已连接的 Auto；确认后保存账号，不自动启动登录。

支持 UTF-8（含 BOM）、Windows CRLF 换行；每批最多 1 MiB、1000 行账号，忽略空行与 `#` 开头的注释行。邮箱、密码和其他字段使用 `----` 分隔，字段本身不能含该分隔符。

两列／三列使用页面选择的 Google 或微软登录方式；六列自动识别为微软，预览逐行显示最终登录方式。

```text
# 两列账号（合成示例）
one@example.test----example-password-1
two@example.test----example-password-2

# 三列账号（合成示例）
three@example.test----example-password-3----backup@example.test
```

微软六列顺序为「邮箱、密码、Client ID、Refresh Token、辅助邮箱、附加字段」。Client ID 须为 UUID，Refresh Token 非空；这些校验只确认格式，不证明登录或 OAuth 凭据有效。第六列用途未确认，仅保留，不作为辅助邮箱密码使用，也不自动读取邮件。

预览返回行号、脱敏邮箱、最终登录方式和校验结果，不返回密码、辅助邮箱、Client ID、Refresh Token、附加字段或原始行。输入文本只保留在当前弹窗中，关闭或导入成功后清空。

错误行需修正后重新预览，确认按钮在存在错误时不可用；服务端保存时也会重新校验，错误输入不会建立部分批次。重复邮箱忽略大小写，按第一行处理，已有账号保持原凭据，重复项跳过。数据库保存异常时回滚本批；全部重复时不创建空批次。

编辑文本、批次名或登录方式后，旧预览失效，须重新预览。预览与保存之间若有其他导入，保存时仍按邮箱唯一约束去重，实际新增数量以保存结果为准。

接口沿用 Manager 到 Auto 的认证转发：`POST /api/batches/preview` 接收与 `POST /api/batches` 相同的 `name`、`text`、`login_provider`。预览为只读操作；Auto 接口要求 Bearer 连接密钥，响应禁止缓存。
