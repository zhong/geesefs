[English](README.md) | 简体中文

<img src="doc/geesefs.png" height="64" width="64" align="middle" />

GeeseFS 是一个用 Go 编写的高性能、类 POSIX 的 S3
([Yandex](https://cloud.yandex.com/en-ru/services/storage)、[Amazon](https://aws.amazon.com/s3/))
文件系统

# 概述

GeeseFS 可以把 S3 bucket 挂载为文件系统。

基于 S3 的 FUSE 文件系统通常存在性能问题，尤其是小文件和元数据操作。

GeeseFS 通过大量并行和异步操作来尝试解决这些问题。

另可参考我们基于 GeeseFS 的 CSI S3 驱动：https://github.com/yandex-cloud/csi-s3

# POSIX 兼容性对比

|                   | GeeseFS | rclone | Goofys | S3FS | gcsfuse |
| ----------------- | ------- | ------ | ------ | ---- | ------- |
| Read after write  |    +    |    +   |    -   |   +  |    +    |
| Partial writes    |    +    |    +   |    -   |   +  |    +    |
| Truncate          |    +    |    -   |    -   |   +  |    +    |
| fallocate         |    +    |    -   |    -   |   -  |    -    |
| chmod/chown       |    Y    |    -   |    -   |   +  |    -    |
| fsync             |    +    |    -   |    -   |   +  |    +    |
| Symlinks          |    Y    |    -   |    -   |   +  |    +    |
| Socket files      |    Y    |    -   |    -   |   +  |    -    |
| Device files      |    Y    |    -   |    -   |   -  |    -    |
| Custom mtime      |    Y    |    +   |    -   |   +  |    +    |
| xattr             |    +    |    -   |    +   |   +  |    -    |
| Directory renames |    +    |    +   |    *   |   +  |    +    |
| readdir & changes |    +    |    +   |    -   |   +  |    +    |

**Y** 仅在 Yandex S3 上正确工作。

**\*** Goofys 中的目录重命名仅允许用于条目不超过 1000 个的目录，且该限制是硬编码的。

GeeseFS 的非 POSIX 行为和限制：
* 文件的 mode/owner/group、符号链接、自定义 mtime 以及特殊文件（块设备、字符设备、命名管道、UNIX 套接字）都受支持，但只有在使用 Yandex S3 时才能被正确还原。原因是标准 S3 的列表结果不返回用户元数据，而要为列表中的每个文件额外发起 HEAD 请求来读取这些元数据，会让列表操作变得太慢。
* 特殊文件支持在 Yandex S3 上默认开启（用 `--no-specials` 关闭），在其他存储上默认关闭。
* 即使是 Yandex S3，文件的 mode/owner/group 默认也是禁用的（用 `--enable-perms` 开启）。禁用时，可以用 `--(dir|file)-mode` 和 `--(uid|gid)` 选项设置全局权限。
* 即使是 Yandex S3，自定义修改时间默认也是禁用的（用 `--enable-mtime` 开启）。禁用时：
  - `ctime`、`atime` 和 `mtime` 始终相同
  - 用户无法设置文件的修改时间（例如 `cp --preserve`、`rsync -a` 或 utimes(2)）
* 不支持硬链接
* 不支持加锁
* 不支持"隐藏"的已删除文件。如果应用在删除文件后仍保持文件描述符打开，之后对它的文件系统操作会返回 ENOENT 错误

除上述内容外：
* 默认的文件大小上限为 1.03 TB，通过将文件切分为 1000 个 5 MB 分片、1000 个 25 MB 分片和 8000 个 125 MB 分片实现。可以修改分片大小，但 AWS 自身的上限是 5 TB。

# 稳定性

GeeseFS 足够稳定，能够通过大部分适用的 `xfstests` 测试，包括 dirstress/fsstress 压力测试（generic/007、generic/011、generic/013）。

另见[常见问题](#常见问题)。

# 性能特性

|                                | GeeseFS | rclone | Goofys | S3FS | gcsfuse |
| ------------------------------ | ------- | ------ | ------ | ---- | ------- |
| Parallel readahead             |    +    |    -   |    +   |   +  |    -    |
| Parallel multipart uploads     |    +    |    -   |    +   |   +  |    -    |
| No readahead on random read    |    +    |    -   |    +   |   -  |    +    |
| Server-side copy on append     |    +    |    -   |    -   |   *  |    +    |
| Server-side copy on update     |    +    |    -   |    -   |   *  |    -    |
| Partial object updates         |    +*   |    -   |    -   |   -  |    -    |
| xattrs without extra RTT       |    +*   |    -   |    -   |   -  |    +    |
| Dir preload on file lookup     |    +    |    -   |    -   |   -  |    -    |
| Fast recursive listings        |    +    |    -   |    *   |   -  |    +    |
| Asynchronous write             |    +    |    +   |    -   |   -  |    -    |
| Asynchronous delete            |    +    |    -   |    -   |   -  |    -    |
| Asynchronous rename            |    +    |    -   |    -   |   -  |    -    |
| Disk cache for reads           |    +    |    *   |    -   |   +  |    +    |
| Disk cache for writes          |    +    |    *   |    -   |   +  |    -    |

\* Goofys 的递归列表优化存在 bug，在某些条件下可能跳过文件

\* S3FS 使用服务端复制，但更新文件时仍需下载整个文件。它也存在 bug :-)

\* rclone mount 有 VFS 缓存，但只能缓存整个文件。而且它也有 bug，写入时经常挂起。

\* 无额外 RTT 的 xattr 仅在 Yandex S3 上可用（`--list-type=ext-v1`）。

\* 部分对象更新（PATCH）仅在 Yandex S3 上可用。

## 部分对象更新（PATCH）

在 Yandex S3 上，可以在不做服务端复制或重新上传的情况下，对对象进行部分更新（仅限数据）。

目前可以通过 `--enable-patch` 开启该功能，将来会对 YC S3 默认开启。

开启 patch 上传有以下好处：
- 快速 [fsync](#fsync)：由于无需复制任何内容，fsync 的开销大大降低
- 支持[并发更新](#并发-patch)
- 更好的内存利用率：需要缓存的中间状态更少，因此可以把更多内存用于（元）数据缓存
- 大文件的性能更好

注意：新文件、元数据变更和重命名仍然会以分片上传（multipart upload）的方式刷新到 S3。

# 本分支的功能增强

本分支（`zhong/geesefs`，分支 `feature/max-disk-cache-size`）在上游 GeeseFS 的基础上增加了以下功能。

## 磁盘缓存大小限制（`--max-disk-cache-size`）

只使用 `--cache` 时，缓存目录会一直增长，直到磁盘写满。`--max-disk-cache-size` 用于限制缓存目录的总大小，单位为 MiB（默认 `0`，即不限制）。超过上限时，会删除最久未使用的缓存文件，直到缓存回到上限以下：

```
geesefs --cache /var/cache/geesefs/my-bucket --max-disk-cache-size 51200 my-bucket /mnt/my-bucket
```

只会淘汰已经上传到 S3 的数据，因此淘汰不会丢失数据。这个上限是软限制：当所有缓存数据都处于打开或脏状态时，缓存可能暂时超过上限。

实现原理：

- **记账。** 每个 inode 记录自己的缓存文件占用的字节数，即目前写入过的范围（extent）。重写已缓存的范围不会重复计数。所有 inode 的总量保存在 `FDQueue` 中，写入、重命名和删除时更新。
- **LRU。** `FDQueue` 现在有两个 LRU 队列。原有的队列跟踪打开的缓存文件描述符（`--max-disk-cache-fd`）。新增的大小队列跟踪所有在磁盘缓存中有数据的 inode。写入、从磁盘缓存读取和重新打开都会把 inode 移到最近使用的一端。
- **淘汰。** 总量超过上限时，`FDCloser` 协程从最久未使用的一端遍历大小队列，删除每个符合条件的 inode 的整个缓存文件。只有满足以下条件的 inode 才会被淘汰：已关闭、不在刷新或从磁盘缓存回读中、未被固定（pinned）、且没有脏缓冲区。这样它的数据已经在 S3 中，可以再次获取。
- **重试。** 即使没有新的写入，淘汰条件也可能改变，例如文件被关闭或刷新完成。因此，缓存超过上限期间，`DiskCacheRetrier` 每秒唤醒一次淘汰逻辑。
- **故障处理。** 如果缓存文件无法重命名，它会留在原路径上，并且该路径被保留，其他文件不会使用它。如果无法删除，它占用的字节仍然计入总量，路径也保持保留，直到后台重试成功删除它。如果某个 inode 没有缓存数据，却在同一路径找到了旧文件，该文件会在第一次被打开时被截断。上一次运行遗留在缓存目录中的文件，在被打开之前不会计入总量，因此每个挂载点请使用一个空目录。
- **关闭。** `Shutdown` 会等待淘汰和重试协程结束，因此它返回之后不会再删除任何缓存文件。

相关测试位于 `core/fd_queue_test.go` 和 `core/goofys_test.go`。

## 部署示例

- [contrib/systemd](contrib/systemd)：模板 unit，用于在 Ubuntu 22.04 / 24.04 上以服务形式运行挂载，每个挂载一个实例。
- [contrib/k8s](contrib/k8s)：Dockerfile、在每个节点上挂载 bucket 的 DaemonSet，以及使用该挂载的示例 Pod。

详见[使用](#使用)以及下面的章节。

# 安装

* 预编译二进制文件：
  * [Linux amd64](https://github.com/yandex-cloud/geesefs/releases/latest/download/geesefs-linux-amd64)。
    你可能还需要先安装 FUSE 工具（fuse3 或 fuse 的 RPM/Debian 包）。
  * [Mac amd64](https://github.com/yandex-cloud/geesefs/releases/latest/download/geesefs-mac-amd64)、
    [arm64](https://github.com/yandex-cloud/geesefs/releases/latest/download/geesefs-mac-arm64)。你还需要安装 osxfuse/macfuse 才能使 GeeseFS 工作。
  * [Windows x64](https://github.com/yandex-cloud/geesefs/releases/latest/download/geesefs-win-x64.exe)。
    你需要先安装 [WinFSP](https://winfsp.dev)。
* 或者用 Go 1.13 或更高版本从源码编译：

```ShellSession
$ git clone https://github.com/yandex-cloud/geesefs
$ cd geesefs
$ go build
```

# 使用

```ShellSession
$ cat ~/.aws/credentials
[default]
aws_access_key_id = AKID1234567890
aws_secret_access_key = MY-SECRET-KEY
$ $GOPATH/bin/geesefs <bucket> <mountpoint>
$ $GOPATH/bin/geesefs [--endpoint https://...] <bucket:prefix> <mountpoint> # 只想挂载某个前缀下的对象时
```

你也可以通过 `AWS_ACCESS_KEY_ID` 和 `AWS_SECRET_ACCESS_KEY` 环境变量提供凭据。

要在启动时挂载 S3 bucket，请确保凭据配置在 `root` 用户下，并在 `/etc/fstab` 中加入：

```
bucket    /mnt/mountpoint    fuse.geesefs    _netdev,allow_other,--file-mode=0666,--dir-mode=0777    0    0
```

如果凭据文件不在默认位置，可以添加 `,--shared-config=/path/to/credentials` 指定其他路径。

## systemd 服务（Ubuntu 22.04 / 24.04）

以服务形式运行 GeeseFS，可以在崩溃后自动重启、日志进入 journald，并在关机时干净地卸载。[contrib/systemd](contrib/systemd) 中有一个模板 unit，每个挂载对应一个实例。两个 Ubuntu 版本都自带 FUSE 3，systemd 版本也足够新：

```
sudo apt install fuse3
sudo install -m 755 geesefs /usr/local/bin/geesefs
sudo install -m 644 contrib/systemd/geesefs@.service /etc/systemd/system/
sudo install -d -m 755 /etc/geesefs
sudo install -m 600 contrib/systemd/example.env /etc/geesefs/my-bucket.env
sudo editor /etc/geesefs/my-bucket.env    # bucket、挂载点、凭据、选项
sudo systemctl daemon-reload
sudo systemctl enable --now geesefs@my-bucket
journalctl -u geesefs@my-bucket -f
```

注意事项：

- unit 以 root 运行 geesefs，这是 `-o allow_other` 所需要的。用 `--uid`、`--gid`、`--file-mode` 和 `--dir-mode` 决定谁可以访问文件。
  使用 `--uid`/`--gid` 时，geesefs 挂载后也会切换到该用户，此时它无法写入只有 root 可以访问的缓存目录。加上 `--setuid 0 --setgid 0` 可以让它继续以 root 运行。
- 每个实例都有独立的磁盘缓存目录 `/var/cache/geesefs/<name>`。设置 `--max-disk-cache-size`（单位 MiB）可以防止它占满磁盘。
- `systemctl stop` 会先卸载，然后等待已修改的文件全部刷新到 S3，最长 10 分钟（`TimeoutStopSec`）。如果停止时仍有进程在使用挂载点，服务会等待该进程退出。
- 使用挂载的其他 unit 可以通过 `Requires=geesefs@my-bucket.service` 和 `After=geesefs@my-bucket.service` 依赖它。只有 bucket 挂载完成后，服务才会变为 active 状态。
- 阿里云 OSS 需要 `--subdomain` 和 OSS 地域，例如 `--endpoint https://oss-cn-beijing-internal.aliyuncs.com --subdomain --region oss-cn-beijing`（这里使用的是内网 endpoint，仅适用于同地域的 ECS 等机器；公网访问请使用公网 endpoint）。

## Kubernetes DaemonSet

[contrib/k8s](contrib/k8s) 可以在每个节点上运行 GeeseFS，并把 bucket 挂载到宿主机的 `/mnt/geesefs/<bucket>`。Pod 通过 `/mnt/geesefs` 的 `hostPath` 卷使用它。

```
docker build -f contrib/k8s/Dockerfile -t <registry>/geesefs:<version> .
docker push <registry>/geesefs:<version>
# 在 contrib/k8s/geesefs-daemonset.yaml 中设置镜像、bucket 和选项
kubectl create namespace geesefs
kubectl -n geesefs create secret generic geesefs-credentials \
    --from-literal=AWS_ACCESS_KEY_ID=... --from-literal=AWS_SECRET_ACCESS_KEY=...
kubectl apply -f contrib/k8s/geesefs-daemonset.yaml
kubectl apply -f contrib/k8s/example-pod.yaml
```

注意事项：

- GeeseFS 容器必须是特权容器，这是为了访问 `/dev/fuse`，以及使用 `Bidirectional` 挂载传播，让挂载点在宿主机上可见。
- Pod 必须按 `example-pod.yaml` 的方式挂载父目录 `/mnt/geesefs`，并设置 `mountPropagation: HostToContainer`。这样即使 GeeseFS 在 Pod 启动之后才挂载 bucket，Pod 也能看到它；GeeseFS 重启之后同样如此。不要把挂载点本身用作 `hostPath`：Pod 会保留一份自己的挂载副本，GeeseFS 重启时无法退出，直到该 Pod 被删除，而且 Pod 也看不到新的挂载。
- GeeseFS Pod 重启期间，使用该 bucket 的 Pod 中 bucket 会暂时消失，打开的文件会返回错误。如果 GeeseFS 被杀死，尚未刷新到 S3 的修改会丢失。因此 DaemonSet 使用 `updateStrategy: OnDelete`：先 drain 节点，再逐个节点更新 GeeseFS。
- 容器的内存限制要明显高于 `--memory-limit`，因为元数据和 Go 运行时也会占用内存。否则内核会杀死 GeeseFS，未刷新的修改也会丢失。
- 所有节点挂载的是同一个 bucket。如果要从多个节点写入同一个文件，请先阅读[并发更新](#并发更新)。
- 这是节点级别的挂载，而不是 CSI 驱动，不会为每个 PersistentVolumeClaim 创建卷。如果需要这种方式，请使用 CSI 驱动，例如可以使用 GeeseFS 的 [csi-s3](https://github.com/yandex-cloud/k8s-csi-s3)。

另见：[Azure Blob Storage 使用说明](https://github.com/yandex-cloud/geesefs/blob/master/README-azure.md)（英文）。

## Windows

安装 [WinFSP](https://winfsp.dev) 和 GeeseFS 之后，其他步骤都与 Linux 相同，只是 GeeseFS 无法后台化运行，因此如果想隐藏控制台窗口，需要手动创建系统服务。

凭据可以放在 `C:\Users\<USERNAME>\.aws\credentials` 中，也可以放在任意文件中并通过 `--shared-config file.txt` 指定，或者使用环境变量：

```
set AWS_ACCESS_KEY_ID=...
set AWS_SECRET_ACCESS_KEY=...
```

然后用 `geesefs <bucket> <mountpoint>` 启动 GeeseFS，其中 `<mountpoint>` 可以是一个盘符（如 K:），也可以是一个不存在的目录。示例：

```
geesefs-win-x64.exe testbucket K:
```

# 基准测试

参见 [bench/README.md](bench/README.md)。

# 配置

可调节的选项很多。运行 `geesefs -h` 可以查看完整的选项列表。

# 常见问题

## 内存限制

**0.37.0 版本起新增：** 元数据缓存的内存用量现在也有限制，由元数据引起的 OOM 错误应该已经消失。

GeeseFS 使用内存的用途有两类：

1. **元数据**（文件列表）。每个元数据条目约占用 1 KB。缓存条目总数由 `--entry-limit` 和 `--stat-cache-ttl` 限制，因为未过期的条目无法从缓存中移除。已修改的条目以及仍有打开的文件或目录描述符的条目也永远不会从缓存中移除。默认缓存 TTL 为 60 秒，默认缓存条目上限为 100000，但实际上 GeeseFS 列出文件的速度更快，因此在对很长的 bucket 进行普通列表时，实际上限约为 250000。

2. **数据**。GeeseFS 默认的数据缓存上限为 1 GB（`--memory-limit`）。GeeseFS 的缓存既用于在需要从服务器加载数据时的读缓冲区，也用于应用写入数据时的写缓冲区。

   同时，默认的"大"预读设置为 100 MB，这对线性读取性能来说是最优的。

   然而，这意味着如果有超过 10 个进程同时读取大文件，它们请求的缓冲区可能超过 1000 MB，从而超出内存限制。从 v0.43.0 开始，GeeseFS 允许这种情况发生，但你可以使用挂载选项 `--use-enomem` 关闭它，这样 GeeseFS 会阻止超出内存限制，并向部分进程返回 ENOMEM 错误。

   解决这个问题的另一种方法是提高 `--memory-limit`（例如提高到 4 GB），或者降低 `--read-ahead-large`（例如降到 20 MB）。

## 最大化吞吐量

如果你有大量空闲带宽，并希望获得更高的线性写入速度，请确保同时写入多个文件（而不是只写一个），并使用以下选项启动 geesefs：

```
geesefs --no-checksum --memory-limit 4000 \
    --max-flushers 32 --max-parallel-parts 32 --part-sizes 25
```

这些选项以降低最大文件大小（上限 250 GB，即 10000 个分片 × 25 MB）和增加内存用量为代价提高并行度。如果网络带宽充足，写入速度可以达到约 1.6 GB/s。例如，使用 fio 测试：

```
fio -name=test -ioengine=libaio -direct=1 -bs=4M -iodepth=1 -fallocate=none \
    -numjobs=8 -group_reporting -rw=write -size=10G
```

## 并发更新

GeeseFS 不支持多个主机同时更新同一个文件。如果确实需要这样做，你应当保证：一个主机对修改过的文件调用 `fsync()`，然后等待至少 `--stat-cache-ttl`（默认 1 分钟）后，再允许其他主机开始更新该文件。另一种方法是使用 `setfattr -n .invalidate <filename>` 强制刷新文件/目录缓存，这会让 GeeseFS 从服务器重新检查文件/目录状态。如果不这样做，可能会出现更新丢失（冲突），日志中会有如下报告：

```
main.WARNING File xxx/yyy is deleted or resized remotely, discarding local changes
```

### 并发 PATCH

使用 Yandex S3 时，可以通过 PATCH 方法（`--enable-patch`）从多个主机并发更新同一个对象/文件。但是，并发修改不会回传给客户端，因此你必须停止所有写入并刷新 inode 缓存，才能看到其他主机所做的修改。

强烈建议写入互不重叠的范围，以避免冲突。如果确实存在重叠写入，你必须保证各主机之间相互协调，并在下一次重叠写入之前执行 `fsync()`，以串行化它们的更新。

另外请注意，即使使用 PATCH，当写入与对象分片边界对齐时（默认情况下，前 5 GB 按 5 MB 分片），性能也最好。原因是服务器可能需要对被 patch 的对象分片做内部的读-改-写，而且如果太多并行请求同时修改同一个对象分片，服务器还可能返回错误。这类"PATCH 失败"会在日志中有如下报告：

```
main.WARNING Failed to patch range %d-%d of file %s (inode %d) due to concurrent updates
```

通常情况下，GeeseFS 会重试这类失败的请求。如果你不希望它重试，也可以开启 `--drop-patch-conflicts`，丢弃缓存中的此次更新。

## 异步写入错误

GeeseFS 会在内存（或启用的磁盘缓存）中缓冲更新，并异步刷新，因此写入方不会因为写入失败而收到错误。出现错误时，GeeseFS 会把修改保留在缓存中，并稍后重试刷新到服务器。GeeseFS 会一直尝试刷新数据，直到成功，或者直到你停止 GeeseFS 挂载进程为止。如果变更的数据过多，达到内存限制，写入请求会挂起，直到部分数据被刷新到服务器、腾出一些内存为止。

### fsync

如果想确认你的修改确实已经持久化到服务器，你需要对文件或目录调用 [fsync](https://man7.org/linux/man-pages/man2/fsync.2.html)。对目录调用 `fsync` 会让 GeeseFS 刷新其中的所有修改。这比 Linux 和 POSIX 的行为更严格：在 Linux 和 POSIX 中，对目录执行 fsync 只会刷新其中的目录项（例如重命名的文件）。

如果在 `fsync` 期间发生服务器或网络错误，调用方会收到错误码。

调用 `fsync` 的示例。注意目录和文件都应当以文件的方式打开：

```
#!/usr/bin/python

import sys, os
os.fsync(os.open(sys.argv[1], os.O_RDONLY))
```

命令行工具 `sync` 和 [syncfs](https://man7.org/linux/man-pages/man2/syncfs.2.html) 系统调用在 GeeseFS 上不起作用，因为它们在 FUSE 中根本没有接入。

## 故障排查

如果你在使用 GeeseFS 时遇到任何问题，例如崩溃、挂起或其他异常行为：

- 如果还没有更新，请更新到最新版本
- 检查系统日志（syslog/journalctl）和 dmesg 中来自 GeeseFS 的错误信息
- 尝试以调试模式启动 GeeseFS：`--debug_s3 --debug_fuse --log-file /path/to/log.txt`，复现问题后，通过 Issues 或其他方式把日志发送给我们
- 如果遇到崩溃，你也可以收集 core dump 并发送给我们：
  - 运行 `ulimit -c unlimited`
  - 用 `sudo sysctl -w kernel.core_pattern=/tmp/core-%e.%p.%h.%t` 设置 core dump 路径
  - 设置环境变量 `GOTRACEBACK=crash` 后启动 geesefs

# 许可证

基于 Apache License, Version 2.0 授权

参见 `LICENSE` 和 `AUTHORS`

## 与 S3 的兼容性

geesefs 可与以下服务配合使用：
* Yandex Object Storage（默认）
* Amazon S3
* Ceph（以及基于 Ceph 的 Digital Ocean Spaces、DreamObjects、gridscale 等）
* Minio
* OpenStack Swift
* Azure Blob Storage（虽然它不是 S3）
* Backblaze B2
* Selectel S3

只要实现了分片上传（multipart upload）和分片服务端复制（UploadPartCopy），其他 S3 兼容存储理论上也应当可以工作。

已知**不可用**的服务：
* CloudFlare R2。它在限流方面有问题：超过每秒 5 个请求时，它不返回 HTTP 429 状态码，而是返回 403 Forbidden。

重要提示：如果在非 Yandex S3 上使用，应当以 `--list-type 2` 或 `--list-type 1` 选项挂载 geesefs。

以下后端继承自 Goofys 代码，仍然存在，但已经无法正常工作：
* Google Cloud Storage
* Azure Data Lake Gen1
* Azure Data Lake Gen2

# 参考资料

* [Yandex Object Storage](https://cloud.yandex.com/en-ru/services/storage)
* [Amazon S3](https://aws.amazon.com/s3/)
* [Amazon SDK for Go](https://github.com/aws/aws-sdk-go)
* 其他相关的 FUSE 文件系统
  * [goofys](https://github.com/kahing/goofys)：GeeseFS 是 Goofys 的分支。
  * [s3fs](https://github.com/s3fs-fuse/s3fs-fuse)：另一个流行的 S3 文件系统。
  * [gcsfuse](https://github.com/googlecloudplatform/gcsfuse)：用于 [Google Cloud Storage](https://cloud.google.com/storage/) 的文件系统。Goofys 借用了该项目的一些骨架代码。
* [S3Proxy](https://github.com/andrewgaul/s3proxy)
* [fuse binding](https://github.com/jacobsa/fuse)，也被 `gcsfuse` 使用
