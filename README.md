# V2Board Telegram 群组管理机器人

基于 Go 开发的 Telegram 群组管理机器人，通过读取 V2Board 数据库验证用户套餐状态，自动管理群组成员。

## 功能

- **入群验证**：新用户加群后自动禁言，需通过邮箱+密码验证身份后解禁
- **首次发言验证**：兜底捕获 Bot 上线前已在群里、或加群事件丢失的"漏网之鱼"，发言时自动启动验证
- **LRU 缓存**：每群独立的已验证用户内存缓存，正常发言 0 次 DB 查询，性能与活跃度无关
- **套餐检查**：只有拥有有效套餐的用户才能通过验证
- **定时巡检**：自动检查已验证用户的套餐状态，过期用户自动踢出并在群内合并通知
- **唯一绑定**：同一面板邮箱只能绑定一个 Telegram 账号，防止账号共享
- **套餐详情**：管理员查询和用户状态可显示套餐名称、到期时间等完整信息
- **白名单**：支持配置例外用户，不受规则约束
- **多群多库**：一个 Bot 实例可管理多个群组，各自对应不同的 V2Board 数据库
- **数据库只读**：Bot 仅需 SELECT 权限，绑定关系存储在本地文件
- **防永久封禁**：踢出时使用 Ban+Unban 组合，并设置兜底超时，避免用户残留在封禁列表

## 验证流程

### 入群时
```
用户加群 → 自动禁言 → 群内显示验证按钮
                          ↓
                   点击按钮跳转 Bot 私聊
                          ↓
              ┌─ 数据库 telegram_id 已绑定且套餐有效 → 直接通过
              ├─ 本地绑定且套餐有效 → 直接通过
              └─ 未绑定 → /bind 邮箱 密码 → 验证通过后解禁
                          ↓
                  超时未验证 → 踢出群组并自动解除封禁
```

### 首次发言时（兜底）

老成员、Bot 上线前已在群里的成员、加群事件丢失的成员，**第一次发言**时也会触发验证：

```
群内任意发言 → 命中已验证缓存？ ─是→ 放行，0 次 DB 查询
                    ↓ 否
              查 DB telegram_id / 本地绑定
                    ↓
        ┌─ 有效 → 写入缓存放行
        └─ 无效 → 删除消息 + 禁言 + 弹验证按钮（同入群流程）
```

缓存是按群独立的 LRU，容量由 `cache_size` 控制（默认 5000），重启后通过自然发言重建，不写盘。

### 绑定规则

- 每个面板邮箱**只能绑定一个 Telegram 账号**
- 如需转移绑定，原账号需先执行 `/unbind` 解绑
- 绑定时会根据当前待验证的群组对应的数据库进行查询，确保在哪个群就用哪个库的用户状态

## 命令列表

| 命令 | 说明 | 权限 |
|------|------|------|
| `/start` | 显示帮助信息 | 所有人 |
| `/bind 邮箱 密码` | 绑定面板账户 | 所有人 |
| `/unbind` | 解除绑定 | 所有人 |
| `/status` | 查看套餐状态 | 所有人 |
| `/check` | 手动触发巡检 | 管理员 |
| `/cha TelegramID` | 查询指定用户信息 | 管理员 |

## 部署

### 1. 下载

从 [Releases](../../releases) 页面下载对应平台的二进制文件。

### 2. 配置

复制示例配置到工作目录，编辑 `config.yaml`：

```bash
cp configs/config.example.yaml config.yaml
```

完整示例：

```yaml
telegram:
  bot_token: "YOUR_BOT_TOKEN"
  admin_ids:
    - 123456789

  # Bot 介绍卡片与命令菜单（可选，启动时自动同步到 Telegram）
  profile:
    description: |
      🤖 V2Board 群组管理机器人

      用于自动管理群组成员，只有拥有有效套餐的用户才能加入。
    short_description: "V2Board 群组管理助手"
    commands:
      - command: "start"
        description: "开始使用"
      - command: "bind"
        description: "绑定面板账户"
      - command: "unbind"
        description: "解除绑定"
      - command: "status"
        description: "查看套餐状态"

groups:
  - chat_id: -1001234567890
    database:
      host: "127.0.0.1"
      port: 3306
      user: "bot_readonly"
      password: "your_password"
      dbname: "v2board"
    exempt_users:
      - 111111111
    verify_timeout: 300

check_interval: 300
```

### 3. 创建数据库只读账号

Bot 需要读取 `v2_user`（用户信息）和 `v2_plan`（套餐名称）两张表：

```sql
CREATE USER 'bot_readonly'@'127.0.0.1' IDENTIFIED BY 'your_password';
GRANT SELECT ON v2board.v2_user TO 'bot_readonly'@'127.0.0.1';
GRANT SELECT ON v2board.v2_plan TO 'bot_readonly'@'127.0.0.1';
FLUSH PRIVILEGES;
```

### 4. Telegram 设置

1. 通过 [@BotFather](https://t.me/BotFather) 创建 Bot 并获取 Token
2. 将 Bot 添加到群组并设为管理员
3. 赋予权限：**禁言用户、封禁用户、删除消息**
4. **重要**：通过 [@BotFather](https://t.me/BotFather) 的 `/setprivacy` 命令把 Bot 的 Privacy Mode 设为 **Disabled**，否则 Bot 收不到群里普通成员的消息，"首次发言验证"功能将失效

### 5. 运行

```bash
chmod +x v2board-tg-bot
./v2board-tg-bot -config config.yaml -bindings bindings.json
```

参数说明：

- `-config` 配置文件路径，默认 `config.yaml`
- `-bindings` 本地绑定存储文件路径，默认 `bindings.json`

### 6. 使用 Supervisor 守护进程（宝塔面板）

在宝塔面板安装 Supervisor 管理器，添加守护进程：

- 名称：`tg-bot`
- 运行目录：`/opt/tg-bot`
- 启动命令：`/opt/tg-bot/v2board-tg-bot -config /opt/tg-bot/config.yaml`

## 配置说明

| 字段 | 说明 |
|------|------|
| `telegram.bot_token` | Telegram Bot Token |
| `telegram.admin_ids` | 管理员 Telegram ID 列表 |
| `telegram.profile.description` | 用户首次打开对话时的介绍卡片（≤ 512 字符） |
| `telegram.profile.short_description` | Bot 个人资料页简介（≤ 120 字符） |
| `telegram.profile.commands` | 命令菜单列表（用户点 `/` 按钮时显示） |
| `groups[].chat_id` | 群组 ID（负数） |
| `groups[].database` | V2Board 数据库连接信息 |
| `groups[].exempt_users` | 白名单用户 Telegram ID 列表 |
| `groups[].verify_timeout` | 验证超时时间（秒），默认 300 |
| `check_interval` | 定时巡检间隔（秒），默认 300 |
| `cache_size` | 每群已验证用户的 LRU 缓存上限，默认 5000 |

`profile` 字段会在启动时同步到 Telegram，同时 `/start` 命令的响应会使用这些内容自动生成。头像和介绍图片仍需通过 [@BotFather](https://t.me/BotFather) 手动设置。

## 项目结构

```
v2board-tg-bot/
├── cmd/bot/                    # 程序入口
│   └── main.go
├── internal/                   # 内部包，不对外公开
│   ├── config/                 # 配置加载与类型定义
│   ├── db/                     # 数据库客户端与密码校验
│   ├── binding/                # 本地绑定存储 (JSON)
│   └── bot/                    # Telegram 业务逻辑
│       ├── handler.go          #   命令与事件处理
│       ├── checker.go          #   定时巡检
│       └── helpers.go          #   格式化工具
├── configs/
│   └── config.example.yaml     # 示例配置
├── .github/workflows/build.yml # CI 构建与发布
├── go.mod
└── README.md
```

本地开发构建：

```bash
go build -o v2board-tg-bot ./cmd/bot
```

## 技术栈

- Go 1.23+
- [telebot v3](https://github.com/tucnak/telebot) - Telegram Bot 框架
- [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) - MySQL 驱动
- [yaml.v3](https://github.com/go-yaml/yaml) - YAML 配置解析

## License

MIT
