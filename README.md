# qin — 版本控制系统

qin 是一个轻量级、内容寻址的版本控制系统，使用 Go 语言编写。它采用 SHA256 内容哈希作为对象标识，支持智能大文件分块（CDC）、跨平台文件变体、HTTP/SSH 远程同步、子模块等特性。

## 解决的痛点

- **跨平台文件管理的问题**
  - qin: 不同平台可有平台相关的文件，克隆时只会将全平台适用和本平台的文件克隆下来，其它平台的文件仅可通过命令查看
  - 其它: 不同平台文件混在一起，到别的平台克隆时会出现这样那样的问题。或者象chromium项目提供工具，仅同步平台相关的东西，比较麻烦
- **大文件传输的问题**
  - qin: 大文件智能分块，仅变动的块会上传
  - 其它: 整个处理，传输慢

## 特性

- **内容寻址存储** — 所有对象通过 SHA256 哈希寻址，自动去重
- **CDC 智能分块** — 基于 Gear hash 的内容定义分块，大文件仅传输变更的分块
- **LFS 懒加载** — 大文件分块可按需拉取，克隆时跳过，适合大型二进制文件
- **跨平台文件变体** — 同一文件可为不同操作系统维护独立版本（win/mac/linux）
- **子模块支持** — 引用外部仓库作为嵌套子目录，固定提交哈希
- **HTTP/SSH 远程同步** — 支持通过 HTTP 或 SSH 协议推送、拉取、克隆
- **三路合并** — BFS 寻找合并基础，自动合并或标记冲突
- **补丁系统** — 生成和应用补丁文件
- **储藏** — 暂存当前工作区变更
- **变基与拣选** — rebase 和 cherry-pick 操作
- **`.qinignore`** — 类似 `.gitignore` 的忽略规则

## 安装

```bash
go install github.com/zhsoft88/qin/cmd/qin@latest
```

或从源码构建：

```bash
git clone https://github.com/zhsoft88/qin
cd qin
mkdir -p dist && go build -o dist/qin ./cmd/qin
```

## 快速入门

```bash
# 初始化仓库
qin init

# 添加文件并提交
qin add file.txt
qin commit -m "first commit"

# 查看状态和历史
qin status
qin log

# 创建分支
qin branch feature
qin switch feature

# 远程协作
qin remote add origin http://example.com/repo
qin push origin
qin pull origin
qin clone http://example.com/repo myrepo
```

## 命令参考

### 基础命令

| 命令 | 说明 |
|------|------|
| `init` | 在当前目录初始化新仓库 |
| `add <file> [--os]` | 暂存文件（`--os` 标记为当前操作系统变体） |
| `rm <file>` | 移除已暂存文件 |
| `commit -m <msg>` | 从暂存区创建提交 |
| `status` | 查看工作区状态 |
| `log [--graph]` | 查看提交历史（`--graph` 分支可视化） |
| `diff [--cached] [<ref> <ref>]` | 查看文件级别变更 |
| `cat <hash>` | 打印对象内容 |
| `ls` | 列出已暂存文件 |
| `show <file> [--os <os>]` | 查看文件的指定 OS 变体内容 |

### 分支与标签

| 命令 | 说明 |
|------|------|
| `branch` | 列出、创建或删除分支 |
| `switch <branch>` | 切换到已有分支 |
| `checkout <ref>` | 检出指定提交的文件 |
| `tag [name]` | 列出或创建标签 |
| `merge <branch>` | 合并分支到当前分支 |
| `rebase <branch>` | 将当前分支变基到目标分支 |
| `cherry-pick <ref>` | 应用指定提交的变更 |

### 远程协作

| 命令 | 说明 |
|------|------|
| `remote add <name> <url>` | 添加远程仓库 |
| `remote remove <name>` | 移除远程 |
| `remote list` | 列出远程 |
| `push [<remote>]` | 推送本地分支到远程 (默认 origin) |
| `fetch [<remote>]` | 从远程拉取对象和引用 |
| `pull [<remote>]` | 拉取并合并远程变更 |
| `clone [--lazy] [--recursive] <url> <dir>` | 克隆远程仓库 |
| `serve [--addr <addr>]` | 启动 HTTP 服务器供远程访问 (默认 :8080) |

### 高级操作

| 命令 | 说明 |
|------|------|
| `stash [pop\|list]` | 储藏或恢复工作区变更 |
| `reset [--soft\|--mixed\|--hard] [<commit>]` | 重置 HEAD/索引/工作区 |
| `restore [--staged] <file>` | 恢复工作区或索引文件 |
| `apply [<patchfile>]` | 应用补丁文件（默认从 stdin 读取） |
| `config [<key> [<value>]]` | 查看或设置配置项 |
| `config --unset <key>` | 重置配置项为默认值 |

### 大文件 (LFS)

| 命令 | 说明 |
|------|------|
| `lfs status` | 查看大文件状态（占位符 / 可用） |
| `lfs pull [--all \| <file>]` | 拉取大文件分块 |

### 子模块

| 命令 | 说明 |
|------|------|
| `submodule add <url> <path>` | 添加子模块 |
| `submodule update [--init]` | 更新或初始化子模块 |
| `submodule status` | 查看子模块状态 |

### 跨平台文件

qin 内建跨平台文件变体支持，可为不同操作系统维护同一文件的独立版本：

```bash
# 添加当前 OS 定制的文件变体
qin add --os config.ini

# 查看文件的所有变体
qin show config.ini

# 查看特定变体的内容
qin show config.ini --os win
qin show config.ini --os linux
```

## 技术细节

### 对象存储

对象存储在 `.qin/objects/XX/YYYYYY` 路径（类似 Git 的布局），内容格式为：

```
gzip(type_byte + varint(content_size) + JSON_content)
```

对象类型：
- **blob (1)** — 文件原始内容或数据块
- **tree (2)** — 目录快照（有序的文件条目列表）
- **commit (3)** — 提交快照（tree + parents + author + message + time）
- **chunk_manifest (4)** — 大文件分块映射

### CDC 智能分块

使用 Gear hash 算法的内容定义分块（Content-Defined Chunking）：

- 分块大小由 `min`/`avg`/`max` 三个参数控制
- 默认值：最小值 1MB，平均值 4MB，最大值 8MB
- 插入或删除数据只影响附近的分块，有效支持增量传输
- 可通过 `qin config` 调整分块参数：

```bash
qin config core.chunk_size_min     # 查看最小值
qin config core.chunk_size 2097152  # 调整为 2MB 平均分块
```

### LFS 懒加载

当克隆大文件仓库时，`--lazy` 选项会跳过实际分块数据的传输，文件在磁盘上以 `qin-lfs` 占位符标记。之后可按需拉取：

```bash
# 懒克隆
qin clone --lazy http://example.com/large-repo myrepo

# 查看大文件状态
qin lfs status

# 拉取所有大文件
qin lfs pull --all

# 拉取特定文件
qin lfs pull large-file.bin
```

### 子模块

子模块允许在仓库中引用外部仓库作为嵌套目录。子模块条目使用 `0160000` 模式标记，哈希值固定为子模块仓库的提交哈希（不存储在当前仓库的对象存储中）。

```lomodules
{
  "submodules": {
    "lib/common": {
      "url": "https://github.com/user/common-lib"
    }
  }
}
```

### 配置

配置文件位于 `.qin/config`，支持以下键：

| 键 | 说明 |
|------|------|
| `core.chunk_min_size` | 最小分块大小（字节），默认 1048576 |
| `core.chunk_threshold` | 分块阈值（字节），默认 4194304 |
| `core.chunk_max_size` | 最大分块大小（字节），默认 8388608 |
| `diff.max_size` | 跳过内容 diff 的文件大小阈值（字节），默认 524288 |
| `diff.max_lines` | 跳过行级 diff 的行数阈值，默认 2000 |
| `user.name` | 提交作者姓名 |
| `user.email` | 提交作者邮箱 |

## 许可证

MIT
