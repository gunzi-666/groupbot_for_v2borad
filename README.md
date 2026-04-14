# V2Board Telegram 群组管理机器人

基于 Go 开发的 Telegram 群组管理机器人，通过读取 V2Board 数据库验证用户套餐状态，自动管理群组成员。

## 功能

- **入群验证**：新用户加群后自动禁言，需通过邮箱+密码验证身份后解禁
- **套餐检查**：只有拥有有效套餐的用户才能通过验证
- **定时巡检**：自动检查已验证用户的套餐状态，过期用户自动踢出并通知
- **白名单**：支持配置例外用户，不受规则约束
- **多群多库**：一个 Bot 实例可管理多个群组，各自对应不同的 V2Board 数据库
- **数据库只读**：Bot 仅需 SELECT 权限，绑定关系存储在本地文件

## 验证流程

```
用户加群 → 自动禁言 → 群内显示验证按钮
                          ↓
                   点击按钮跳转 Bot 私聊
                          ↓
              ┌─ 已绑定且套餐有效 → 直接通过，解禁
              └─ 未绑定 → /bind 邮箱 密码 → 验证通过后解禁
                          ↓
                  超时未验证 → 踢出群组
```

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

创建 `config.yaml`：

```yaml
telegram:
  bot_token: "YOUR_BOT_TOKEN"
  admin_ids:
    - 123456789

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

```sql
CREATE USER 'bot_readonly'@'127.0.0.1' IDENTIFIED BY 'your_password';
GRANT SELECT ON v2board.v2_user TO 'bot_readonly'@'127.0.0.1';
FLUSH PRIVILEGES;
```

### 4. Telegram 设置

1. 通过 [@BotFather](https://t.me/BotFather) 创建 Bot 并获取 Token
2. 将 Bot 添加到群组并设为管理员
3. 赋予权限：禁言用户、封禁用户、删除消息

### 5. 运行

```bash
chmod +x v2board-tg-bot
./v2board-tg-bot -config config.yaml
```

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
| `groups[].chat_id` | 群组 ID（负数） |
| `groups[].database` | V2Board 数据库连接信息 |
| `groups[].exempt_users` | 白名单用户 Telegram ID 列表 |
| `groups[].verify_timeout` | 验证超时时间（秒），默认 300 |
| `check_interval` | 定时巡检间隔（秒），默认 300 |

## 技术栈

- Go 1.21+
- [telebot v3](https://github.com/tucnak/telebot) - Telegram Bot 框架
- [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) - MySQL 驱动
- [yaml.v3](https://github.com/go-yaml/yaml) - YAML 配置解析

## License

MIT
