# Alist-Oasis 部署教程

## 一、快速部署（推荐）

### 1. 一键安装 Alist

```bash
bash <(curl -s https://raw.githubusercontent.com/oasis168/alist/main/install.sh) install
```

安装完成后访问 `http://你的IP:5244`，默认用户名 `admin`，密码见安装日志。

---

## 二、百度网盘索引导入

### 背景

Alist-Oasis 支持导入百度网盘客户端导出的本地缓存数据库（`.db` 文件），实现千万级文件秒搜，无需逐页调用百度 API。

### 步骤

#### 1. 导出百度网盘本地缓存 `.db` 文件

在安装了百度网盘客户端的 Windows 电脑上，找到以下目录：

```
C:\Users\你的用户名\AppData\Roaming\baidu\BaiduNetdisk\
```

找到 `*.db` 文件（通常名为 `filedata.db` 或类似名称），复制到本地备用。

#### 2. 在 Alist 后台配置搜索引擎

进入 `管理 → 设置 → 索引`，根据需求选择搜索引擎：

| 搜索引擎 | 适用场景 | 说明 |
|---------|---------|------|
| `database` | 小数据量（< 100万条）| 内置 SQLite，开箱即用，但千万级搜索慢 |
| `meilisearch` | 千万级数据，追求极速 | 需要额外部署 Meilisearch 服务 |
| `bleve` | 中等数据量，无需额外部署 | 内置，但中文分词精度较低 |

#### 3. 挂载百度网盘

进入 `管理 → 存储 → 添加`，选择 `百度网盘`，完成 OAuth 授权，设置挂载路径（如 `/百度网盘`）。

#### 4. 导入 `.db` 文件

进入 `管理 → 存储`，找到对应的百度网盘挂载，点击 **导入索引** 按钮：

- **上传文件**：直接上传本地 `.db` 文件
- **服务器路径**：如果 `.db` 文件已在服务器上，填写绝对路径（如 `/opt/alist/db/filedata.db`）

导入过程中可实时查看进度条（已导入/总条数）。刷新页面后进度会自动恢复显示。

导入完成后，搜索功能即可使用，结果秒出。

---

## 三、使用 Meilisearch 实现千万级秒搜（推荐）

### 1. 安装 Meilisearch

#### 方式 A：Docker（推荐）

```bash
docker run -d \
  --name meilisearch \
  --restart always \
  -p 7700:7700 \
  -v /opt/meilisearch/data:/meili_data \
  -e MEILI_MASTER_KEY=your_master_key_here \
  getmeili/meilisearch:latest
```

#### 方式 B：直接安装

```bash
curl -L https://install.meilisearch.com | sh
mv ./meilisearch /usr/local/bin/

# 创建 systemd 服务
cat > /etc/systemd/system/meilisearch.service << EOF
[Unit]
Description=Meilisearch
After=network.target

[Service]
ExecStart=/usr/local/bin/meilisearch --db-path /opt/meilisearch/data --master-key your_master_key_here
Restart=always
WorkingDirectory=/opt/meilisearch

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now meilisearch
```

### 2. 在 Alist 后台配置 Meilisearch

> **重要：配置参数必须在选择 meilisearch 引擎之前填写，否则 host/key 不会生效。**

**正确操作步骤：**

1. 进入 `管理 → 设置 → 索引`
2. 先将 **SearchIndex 设为 `none`** 并保存
3. 等待 2 秒
4. 填写以下参数：

| 配置项 | 值 | 说明 |
|--------|-----|------|
| Meilisearch Host | `http://localhost:7700` | Meilisearch 服务地址 |
| Meilisearch API Key | `your_master_key_here` | 与启动时的 master key 一致 |
| Meilisearch Index Prefix | （留空）| 单实例留空即可；多个 Alist 共用同一个 Meilisearch 时才需要填，用于区分不同实例，如 `site1_` |

5. 将 **SearchIndex 改为 `meilisearch`** 并保存
6. 在存储页面找到对应的百度网盘，点击**导入索引**按钮上传 `.db` 文件

保存后等待导入完成即可使用秒搜。

### 3. docker-compose 一键部署（Alist + Meilisearch）

```yaml
services:
  alist:
    image: xhofe/alist:latest
    restart: always
    ports:
      - "5244:5244"
    volumes:
      - ./data:/opt/alist/data
    environment:
      - PUID=0
      - PGID=0
      - UMASK=022

  meilisearch:
    image: getmeili/meilisearch:latest
    restart: always
    ports:
      - "127.0.0.1:7700:7700"
    volumes:
      - ./meili_data:/meili_data
    environment:
      - MEILI_MASTER_KEY=your_master_key_here
```

```bash
docker-compose up -d
```

Alist 后台 Meilisearch Host 填 `http://meilisearch:7700`（内网互通，7700 不对外暴露）。

---

## 四、注意事项

1. **重新导入**：每次更换搜索引擎后，需要先清空索引，再重新导入 `.db` 文件。
2. **定期更新**：百度网盘文件变化后，需重新导出 `.db` 并导入，或使用 Alist 的增量索引功能。
3. **内存要求**：
   - 100万条数据：Meilisearch 约需 512MB 内存
   - 1000万条数据：Meilisearch 约需 2GB 内存
   - 磁盘：每百万条数据约占 200-500MB
4. **安全**：Meilisearch 的 7700 端口不要对外暴露，只需 Alist 内网访问即可。
