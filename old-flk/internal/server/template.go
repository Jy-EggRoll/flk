// template.go 定义 Web 界面的 HTML 模板
// 使用 Go 的原始字符串（raw string）嵌入 HTML，避免外部依赖
package server

// indexHTML 首页 HTML 模板
// 使用现代 CSS 和原生 JavaScript 实现响应式界面
const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>文件链接管理器 - Web 界面</title>
    <style>
        /* ========== 全局样式 ========== */
        /* 使用 CSS 变量定义主题颜色，便于统一修改 */
        :root {
            --primary-color: #4a90d9;      /* 主色调：蓝色 */
            --success-color: #52c41a;      /* 成功色：绿色 */
            --error-color: #f5222d;        /* 错误色：红色 */
            --warning-color: #faad14;      /* 警告色：橙色 */
            --bg-color: #f5f7fa;           /* 背景色：浅灰 */
            --card-bg: #ffffff;            /* 卡片背景：白色 */
            --text-color: #333333;         /* 文字颜色：深灰 */
            --border-color: #e8e8e8;       /* 边框颜色：浅灰 */
            --shadow: 0 2px 8px rgba(0,0,0,0.1); /* 阴影效果 */
        }

        /* 重置默认样式 */
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        /* 页面主体样式 */
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background-color: var(--bg-color);
            color: var(--text-color);
            line-height: 1.6;
            padding: 20px;
        }

        /* ========== 容器样式 ========== */
        .container {
            max-width: 1200px;      /* 最大宽度限制 */
            margin: 0 auto;         /* 水平居中 */
        }

        /* 页眉样式 */
        header {
            text-align: center;
            margin-bottom: 30px;
            padding: 20px;
            background: linear-gradient(135deg, var(--primary-color), #667eea);
            color: white;
            border-radius: 10px;
            box-shadow: var(--shadow);
        }

        header h1 {
            font-size: 2rem;
            margin-bottom: 10px;
        }

        header p {
            opacity: 0.9;
        }

        /* ========== 卡片样式 ========== */
        .card {
            background: var(--card-bg);
            border-radius: 10px;
            padding: 20px;
            margin-bottom: 20px;
            box-shadow: var(--shadow);
        }

        .card-title {
            font-size: 1.25rem;
            font-weight: 600;
            margin-bottom: 15px;
            padding-bottom: 10px;
            border-bottom: 1px solid var(--border-color);
            display: flex;
            justify-content: space-between;
            align-items: center;
        }

        /* ========== 统计信息样式 ========== */
        .stats {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
            gap: 15px;
            margin-bottom: 20px;
        }

        .stat-item {
            text-align: center;
            padding: 15px;
            background: var(--bg-color);
            border-radius: 8px;
        }

        .stat-value {
            font-size: 2rem;
            font-weight: bold;
            color: var(--primary-color);
        }

        .stat-label {
            font-size: 0.875rem;
            color: #666;
        }

        .stat-item.success .stat-value { color: var(--success-color); }
        .stat-item.error .stat-value { color: var(--error-color); }

        /* ========== 按钮样式 ========== */
        .btn {
            display: inline-block;
            padding: 10px 20px;
            font-size: 1rem;
            font-weight: 500;
            text-align: center;
            text-decoration: none;
            border: none;
            border-radius: 6px;
            cursor: pointer;
            transition: all 0.3s ease;
        }

        .btn-primary {
            background: var(--primary-color);
            color: white;
        }

        .btn-primary:hover {
            background: #3a7bc8;
            transform: translateY(-1px);
        }

        .btn-success {
            background: var(--success-color);
            color: white;
        }

        .btn-success:hover {
            background: #389e0d;
        }

        .btn:disabled {
            opacity: 0.6;
            cursor: not-allowed;
            transform: none;
        }

        /* ========== 表格样式 ========== */
        .table-container {
            overflow-x: auto;  /* 水平滚动 */
        }

        table {
            width: 100%;
            border-collapse: collapse;
            font-size: 0.9rem;
        }

        th, td {
            padding: 12px 15px;
            text-align: left;
            border-bottom: 1px solid var(--border-color);
        }

        th {
            background: var(--bg-color);
            font-weight: 600;
            position: sticky;
            top: 0;
        }

        tr:hover {
            background: #fafafa;
        }

        /* 状态标签样式 */
        .status {
            display: inline-block;
            padding: 4px 12px;
            border-radius: 20px;
            font-size: 0.8rem;
            font-weight: 500;
        }

        .status-valid {
            background: #f6ffed;
            color: var(--success-color);
            border: 1px solid #b7eb8f;
        }

        .status-invalid {
            background: #fff2f0;
            color: var(--error-color);
            border: 1px solid #ffccc7;
        }

        /* 类型标签样式 */
        .type-tag {
            display: inline-block;
            padding: 2px 8px;
            border-radius: 4px;
            font-size: 0.75rem;
            font-weight: 500;
        }

        .type-symlink {
            background: #e6f7ff;
            color: #1890ff;
        }

        .type-hardlink {
            background: #fff7e6;
            color: #fa8c16;
        }

        /* 路径样式 */
        .path {
            font-family: "Consolas", "Monaco", monospace;
            font-size: 0.85rem;
            word-break: break-all;
            max-width: 300px;
        }

        /* ========== 表单样式 ========== */
        .form-group {
            margin-bottom: 15px;
        }

        .form-group label {
            display: block;
            margin-bottom: 5px;
            font-weight: 500;
        }

        .form-group input,
        .form-group select {
            width: 100%;
            padding: 10px 12px;
            font-size: 1rem;
            border: 1px solid var(--border-color);
            border-radius: 6px;
            transition: border-color 0.3s;
        }

        .form-group input:focus,
        .form-group select:focus {
            outline: none;
            border-color: var(--primary-color);
            box-shadow: 0 0 0 3px rgba(74, 144, 217, 0.1);
        }

        .form-row {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 15px;
        }

        .checkbox-group {
            display: flex;
            align-items: center;
            gap: 8px;
        }

        .checkbox-group input[type="checkbox"] {
            width: auto;
        }

        /* ========== 提示信息样式 ========== */
        .alert {
            padding: 15px;
            border-radius: 6px;
            margin-bottom: 15px;
        }

        .alert-info {
            background: #e6f7ff;
            border: 1px solid #91d5ff;
            color: #1890ff;
        }

        .alert-success {
            background: #f6ffed;
            border: 1px solid #b7eb8f;
            color: var(--success-color);
        }

        .alert-error {
            background: #fff2f0;
            border: 1px solid #ffccc7;
            color: var(--error-color);
        }

        /* ========== 加载动画 ========== */
        .loading {
            display: inline-block;
            width: 16px;
            height: 16px;
            border: 2px solid #f3f3f3;
            border-top: 2px solid var(--primary-color);
            border-radius: 50%;
            animation: spin 1s linear infinite;
            margin-right: 8px;
        }

        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }

        /* ========== 空状态样式 ========== */
        .empty-state {
            text-align: center;
            padding: 40px;
            color: #999;
        }

        .empty-state svg {
            width: 80px;
            height: 80px;
            margin-bottom: 15px;
            opacity: 0.5;
        }

        /* ========== 响应式设计 ========== */
        @media (max-width: 768px) {
            body {
                padding: 10px;
            }

            header h1 {
                font-size: 1.5rem;
            }

            .path {
                max-width: 150px;
            }

            th, td {
                padding: 8px 10px;
            }
        }

        /* ========== 页脚样式 ========== */
        footer {
            text-align: center;
            padding: 20px;
            color: #999;
            font-size: 0.875rem;
        }

        footer a {
            color: var(--primary-color);
            text-decoration: none;
        }

        footer a:hover {
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <div class="container">
        <!-- 页眉 -->
        <header>
            <h1>📁 文件链接管理器</h1>
            <p>管理符号链接和硬链接的 Web 界面</p>
        </header>

        <!-- 统计信息卡片 -->
        <div class="card">
            <div class="card-title">
                <span>📊 链接统计</span>
                <button class="btn btn-primary" id="refreshBtn" onclick="refreshCheck()">
                    🔄 刷新检查
                </button>
            </div>
            <div class="stats" id="statsContainer">
                <div class="stat-item">
                    <div class="stat-value" id="totalSymlinks">-</div>
                    <div class="stat-label">符号链接总数</div>
                </div>
                <div class="stat-item success">
                    <div class="stat-value" id="validSymlinks">-</div>
                    <div class="stat-label">有效符号链接</div>
                </div>
                <div class="stat-item error">
                    <div class="stat-value" id="invalidSymlinks">-</div>
                    <div class="stat-label">无效符号链接</div>
                </div>
                <div class="stat-item">
                    <div class="stat-value" id="totalHardlinks">-</div>
                    <div class="stat-label">硬链接总数</div>
                </div>
                <div class="stat-item success">
                    <div class="stat-value" id="validHardlinks">-</div>
                    <div class="stat-label">有效硬链接</div>
                </div>
                <div class="stat-item error">
                    <div class="stat-value" id="invalidHardlinks">-</div>
                    <div class="stat-label">无效硬链接</div>
                </div>
            </div>
            <div id="messageContainer"></div>
        </div>

        <!-- 链接列表卡片 -->
        <div class="card">
            <div class="card-title">📋 链接列表</div>
            <div class="table-container">
                <table>
                    <thead>
                        <tr>
                            <th>类型</th>
                            <th>源路径</th>
                            <th>目标路径</th>
                            <th>设备</th>
                            <th>状态</th>
                        </tr>
                    </thead>
                    <tbody id="linksTableBody">
                        <tr>
                            <td colspan="5" class="empty-state">
                                点击"刷新检查"按钮加载链接列表
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>

        <!-- 创建链接卡片 -->
        <div class="card">
            <div class="card-title">➕ 创建新链接</div>
            <div class="alert alert-info">
                💡 提示：创建符号链接需要管理员权限，系统会弹出 UAC 提示框请求确认
            </div>
            <form id="createForm" onsubmit="createLink(event)">
                <div class="form-row">
                    <div class="form-group">
                        <label for="linkType">链接类型</label>
                        <select id="linkType" name="type" required>
                            <option value="symlink">符号链接（Symlink）</option>
                            <option value="hardlink">硬链接（Hardlink）</option>
                        </select>
                    </div>
                    <div class="form-group">
                        <label for="device">设备标识（可选）</label>
                        <input type="text" id="device" name="device" placeholder="例如：laptop、desktop、common">
                    </div>
                </div>
                <div class="form-group">
                    <label for="source" id="sourceLabel">源路径（真实文件路径）</label>
                    <input type="text" id="source" name="source" required placeholder="例如：D:\Data\config.json">
                </div>
                <div class="form-group">
                    <label for="target" id="targetLabel">目标路径（链接文件路径）</label>
                    <input type="text" id="target" name="target" required placeholder="例如：C:\Users\用户\AppData\config.json">
                </div>
                <div class="form-group checkbox-group">
                    <input type="checkbox" id="force" name="force">
                    <label for="force">强制覆盖已存在的文件</label>
                </div>
                <button type="submit" class="btn btn-success" id="createBtn">
                    ✨ 创建链接
                </button>
            </form>
            <div id="createResult" style="margin-top: 15px;"></div>
        </div>

        <!-- 页脚 -->
        <footer>
            <p>文件链接管理器（flk） · 按 Ctrl+C 停止服务器</p>
        </footer>
    </div>

    <script>
        // ========== JavaScript 交互逻辑 ==========

        // 页面加载完成后自动刷新检查
        document.addEventListener('DOMContentLoaded', function() {
            refreshCheck();
            updateFormLabels();
        });

        // 链接类型切换时更新表单标签
        document.getElementById('linkType').addEventListener('change', updateFormLabels);

        // 更新表单标签文字
        // 根据选择的链接类型，动态更新源路径和目标路径的标签说明
        function updateFormLabels() {
            var type = document.getElementById('linkType').value;
            var sourceLabel = document.getElementById('sourceLabel');
            var targetLabel = document.getElementById('targetLabel');

            if (type === 'symlink') {
                sourceLabel.textContent = '源路径（真实文件路径）';
                targetLabel.textContent = '目标路径（链接文件路径）';
            } else {
                sourceLabel.textContent = '源路径（主要文件路径）';
                targetLabel.textContent = '目标路径（次要文件路径）';
            }
        }

        // 刷新检查
        // 调用后端 API 获取所有链接的状态
        async function refreshCheck() {
            var btn = document.getElementById('refreshBtn');
            btn.disabled = true;
            btn.innerHTML = '<span class="loading"></span>检查中...';

            try {
                // 发送 POST 请求到 /api/refresh 端点
                var response = await fetch('/api/refresh', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    }
                });

                // 解析 JSON 响应
                var data = await response.json();

                if (data.success) {
                    // 更新统计信息
                    document.getElementById('totalSymlinks').textContent = data.total_symlinks;
                    document.getElementById('validSymlinks').textContent = data.valid_symlinks;
                    document.getElementById('invalidSymlinks').textContent = data.invalid_symlinks;
                    document.getElementById('totalHardlinks').textContent = data.total_hardlinks;
                    document.getElementById('validHardlinks').textContent = data.valid_hardlinks;
                    document.getElementById('invalidHardlinks').textContent = data.invalid_hardlinks;

                    // 更新链接列表
                    updateLinksTable(data.links);

                    // 显示消息
                    showMessage(data.message, 'success');
                } else {
                    showMessage(data.message || '检查失败', 'error');
                }
            } catch (error) {
                showMessage('请求失败：' + error.message, 'error');
            } finally {
                btn.disabled = false;
                btn.innerHTML = '🔄 刷新检查';
            }
        }

        // 更新链接表格
        // 将链接数据渲染到表格中
        function updateLinksTable(links) {
            var tbody = document.getElementById('linksTableBody');

            // 如果没有链接，显示空状态
            if (!links || links.length === 0) {
                tbody.innerHTML = '<tr><td colspan="5" class="empty-state">暂无链接记录</td></tr>';
                return;
            }

            // 构建表格行 HTML
            var html = '';
            links.forEach(function(link) {
                // 根据状态确定样式类
                var statusClass = link.status === 'valid' ? 'status-valid' : 'status-invalid';
                // 根据类型确定样式类
                var typeClass = link.type === 'symlink' ? 'type-symlink' : 'type-hardlink';
                var typeName = link.type === 'symlink' ? '符号链接' : '硬链接';

                html += '<tr>';
                html += '<td><span class="type-tag ' + typeClass + '">' + typeName + '</span></td>';
                html += '<td class="path" title="' + escapeHtml(link.source) + '">' + escapeHtml(link.source) + '</td>';
                html += '<td class="path" title="' + escapeHtml(link.target) + '">' + escapeHtml(link.target) + '</td>';
                html += '<td>' + escapeHtml(link.device || 'common') + '</td>';
                html += '<td><span class="status ' + statusClass + '" title="' + escapeHtml(link.status_text) + '">' + escapeHtml(link.status_text) + '</span></td>';
                html += '</tr>';
            });

            tbody.innerHTML = html;
        }

        // 创建链接
        // 提交表单创建新的符号链接或硬链接
        async function createLink(event) {
            event.preventDefault(); // 阻止表单默认提交行为

            var btn = document.getElementById('createBtn');
            var resultDiv = document.getElementById('createResult');
            btn.disabled = true;
            btn.innerHTML = '<span class="loading"></span>创建中...';

            // 收集表单数据
            var formData = {
                type: document.getElementById('linkType').value,
                source: document.getElementById('source').value.trim(),
                target: document.getElementById('target').value.trim(),
                device: document.getElementById('device').value.trim(),
                force: document.getElementById('force').checked
            };

            try {
                // 发送 POST 请求到 /api/create 端点
                var response = await fetch('/api/create', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify(formData)
                });

                // 解析 JSON 响应
                var data = await response.json();

                if (data.success) {
                    resultDiv.innerHTML = '<div class="alert alert-success">' + escapeHtml(data.message) + '</div>';
                    // 创建成功后刷新列表
                    setTimeout(refreshCheck, 1000);
                    // 清空表单
                    document.getElementById('source').value = '';
                    document.getElementById('target').value = '';
                } else {
                    resultDiv.innerHTML = '<div class="alert alert-error">' + escapeHtml(data.message) + '</div>';
                }

                // 如果有命令输出，显示它
                if (data.output) {
                    resultDiv.innerHTML += '<pre style="background:#f5f5f5;padding:10px;border-radius:4px;overflow-x:auto;font-size:0.85rem;">' + escapeHtml(data.output) + '</pre>';
                }
            } catch (error) {
                resultDiv.innerHTML = '<div class="alert alert-error">请求失败：' + escapeHtml(error.message) + '</div>';
            } finally {
                btn.disabled = false;
                btn.innerHTML = '✨ 创建链接';
            }
        }

        // 显示消息
        // 在消息容器中显示提示信息
        function showMessage(message, type) {
            var container = document.getElementById('messageContainer');
            var alertClass = type === 'success' ? 'alert-success' : (type === 'error' ? 'alert-error' : 'alert-info');
            container.innerHTML = '<div class="alert ' + alertClass + '">' + escapeHtml(message) + '</div>';

            // 5秒后自动隐藏
            setTimeout(function() {
                container.innerHTML = '';
            }, 5000);
        }

        // HTML 转义函数
        // 防止 XSS 攻击，将特殊字符转换为 HTML 实体
        function escapeHtml(text) {
            if (!text) return '';
            var div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }
    </script>
</body>
</html>
`
